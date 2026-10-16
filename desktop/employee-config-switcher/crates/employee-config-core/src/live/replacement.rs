//! Pure sequencing gate for a future existing-file replacement transaction.
//!
//! This module performs no filesystem I/O and is not a durable journal format.
//! It exists to make the reviewed commit/rollback ordering executable before
//! publication primitives or operating-system key providers are introduced.

const MAX_TARGETS: usize = 16;
const MAX_TARGET_NAME_BYTES: usize = 4 * 1024;

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum ReplacementPhase {
    Preparing,
    CommitPrepared,
    RollbackPrepared,
    Complete,
    RolledBack,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum RollbackReason {
    ValidationFailed,
    PermanentPublicationFailure,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct ReplacementTargetState {
    pub backed_up: bool,
    pub staged: bool,
    pub published: bool,
    pub verified: bool,
    pub rollback_staged: bool,
    pub restored: bool,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub enum ReplacementEvent {
    Begin { targets: Vec<String> },
    BackedUp { target: String },
    Staged { target: String },
    CommitPrepared,
    Published { target: String },
    Verified { target: String },
    RollbackPrepared { reason: RollbackReason },
    RollbackStaged { target: String },
    Restored { target: String },
    Complete,
    RolledBack,
}

#[derive(Debug, thiserror::Error, PartialEq, Eq)]
pub enum ReplacementSequenceError {
    #[error("replacement sequence must begin with exactly one Begin event")]
    BeginRequired,
    #[error(
        "replacement target set must contain 1 through 16 sorted unique bounded UTF-8 leaf names"
    )]
    InvalidTargets,
    #[error("replacement event names a target outside the Begin set: {0}")]
    UnknownTarget(String),
    #[error("replacement event duplicates an already recorded transition for: {0}")]
    DuplicateTransition(String),
    #[error("replacement event is not valid in phase {0:?}")]
    InvalidTransition(ReplacementPhase),
    #[error("replacement event precondition is not satisfied: {0}")]
    UnsatisfiedPrecondition(&'static str),
}

#[derive(Debug, Clone, PartialEq, Eq)]
struct TargetProgress {
    name: String,
    backed_up: bool,
    staged: bool,
    published: bool,
    verified: bool,
    rollback_staged: bool,
    restored: bool,
}

/// In-memory reducer for redacted replacement journal event kinds.
///
/// Full durable records will also need identities, digests, lengths and
/// security metadata. This reducer intentionally accepts none of those values,
/// so it cannot publish, restore, serialize or claim transaction durability.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct ReplacementSequence {
    phase: ReplacementPhase,
    targets: Vec<TargetProgress>,
    rollback_reason: Option<RollbackReason>,
}

impl ReplacementSequence {
    pub fn replay(events: &[ReplacementEvent]) -> Result<Self, ReplacementSequenceError> {
        let Some((ReplacementEvent::Begin { targets }, remaining)) = events.split_first() else {
            return Err(ReplacementSequenceError::BeginRequired);
        };
        let mut sequence = Self::begin(targets.clone())?;
        for event in remaining {
            sequence.apply(event)?;
        }
        Ok(sequence)
    }

    pub fn begin(targets: Vec<String>) -> Result<Self, ReplacementSequenceError> {
        validate_targets(&targets)?;
        Ok(Self {
            phase: ReplacementPhase::Preparing,
            targets: targets
                .into_iter()
                .map(|name| TargetProgress {
                    name,
                    backed_up: false,
                    staged: false,
                    published: false,
                    verified: false,
                    rollback_staged: false,
                    restored: false,
                })
                .collect(),
            rollback_reason: None,
        })
    }

    pub fn phase(&self) -> ReplacementPhase {
        self.phase
    }

    pub fn targets(&self) -> impl ExactSizeIterator<Item = &str> {
        self.targets.iter().map(|target| target.name.as_str())
    }

    pub fn rollback_reason(&self) -> Option<RollbackReason> {
        self.rollback_reason
    }

    pub fn target_state(
        &self,
        target: &str,
    ) -> Result<ReplacementTargetState, ReplacementSequenceError> {
        let progress = &self.targets[self.target_index(target)?];
        Ok(ReplacementTargetState {
            backed_up: progress.backed_up,
            staged: progress.staged,
            published: progress.published,
            verified: progress.verified,
            rollback_staged: progress.rollback_staged,
            restored: progress.restored,
        })
    }

    pub fn apply(&mut self, event: &ReplacementEvent) -> Result<(), ReplacementSequenceError> {
        if matches!(
            self.phase,
            ReplacementPhase::Complete | ReplacementPhase::RolledBack
        ) {
            return Err(ReplacementSequenceError::InvalidTransition(self.phase));
        }

        match event {
            ReplacementEvent::Begin { .. } => Err(ReplacementSequenceError::BeginRequired),
            ReplacementEvent::BackedUp { target } => {
                self.require_phase(ReplacementPhase::Preparing)?;
                let index = self.target_index(target)?;
                if self.targets[index].backed_up {
                    return Err(ReplacementSequenceError::DuplicateTransition(
                        target.clone(),
                    ));
                }
                if self.targets.iter().any(|progress| progress.staged) {
                    return Err(ReplacementSequenceError::UnsatisfiedPrecondition(
                        "all backups must precede every desired stage",
                    ));
                }
                self.targets[index].backed_up = true;
                Ok(())
            }
            ReplacementEvent::Staged { target } => {
                self.require_phase(ReplacementPhase::Preparing)?;
                if !self.targets.iter().all(|progress| progress.backed_up) {
                    return Err(ReplacementSequenceError::UnsatisfiedPrecondition(
                        "every target must be backed up before staging",
                    ));
                }
                let index = self.target_index(target)?;
                if self.targets[index].staged {
                    return Err(ReplacementSequenceError::DuplicateTransition(
                        target.clone(),
                    ));
                }
                self.targets[index].staged = true;
                Ok(())
            }
            ReplacementEvent::CommitPrepared => {
                self.require_phase(ReplacementPhase::Preparing)?;
                if !self
                    .targets
                    .iter()
                    .all(|progress| progress.backed_up && progress.staged)
                {
                    return Err(ReplacementSequenceError::UnsatisfiedPrecondition(
                        "every target must be backed up and staged before commit",
                    ));
                }
                self.phase = ReplacementPhase::CommitPrepared;
                Ok(())
            }
            ReplacementEvent::Published { target } => {
                self.require_phase(ReplacementPhase::CommitPrepared)?;
                let index = self.target_index(target)?;
                if self.targets[index].published {
                    return Err(ReplacementSequenceError::DuplicateTransition(
                        target.clone(),
                    ));
                }
                self.targets[index].published = true;
                Ok(())
            }
            ReplacementEvent::Verified { target } => {
                self.require_phase(ReplacementPhase::CommitPrepared)?;
                let index = self.target_index(target)?;
                if !self.targets[index].published {
                    return Err(ReplacementSequenceError::UnsatisfiedPrecondition(
                        "a target must be published before verification",
                    ));
                }
                if self.targets[index].verified {
                    return Err(ReplacementSequenceError::DuplicateTransition(
                        target.clone(),
                    ));
                }
                self.targets[index].verified = true;
                Ok(())
            }
            ReplacementEvent::RollbackPrepared { reason } => {
                self.require_phase(ReplacementPhase::CommitPrepared)?;
                if !self.targets.iter().any(|progress| progress.published)
                    || self.targets.iter().all(|progress| progress.verified)
                {
                    return Err(ReplacementSequenceError::UnsatisfiedPrecondition(
                        "rollback requires a non-fully-verified published prefix",
                    ));
                }
                self.phase = ReplacementPhase::RollbackPrepared;
                self.rollback_reason = Some(*reason);
                Ok(())
            }
            ReplacementEvent::RollbackStaged { target } => {
                self.require_phase(ReplacementPhase::RollbackPrepared)?;
                let index = self.target_index(target)?;
                if !self.targets[index].published {
                    return Err(ReplacementSequenceError::UnsatisfiedPrecondition(
                        "only published targets require rollback stages",
                    ));
                }
                if self.targets[index].rollback_staged {
                    return Err(ReplacementSequenceError::DuplicateTransition(
                        target.clone(),
                    ));
                }
                self.targets[index].rollback_staged = true;
                Ok(())
            }
            ReplacementEvent::Restored { target } => {
                self.require_phase(ReplacementPhase::RollbackPrepared)?;
                if !self
                    .targets
                    .iter()
                    .filter(|progress| progress.published)
                    .all(|progress| progress.rollback_staged)
                {
                    return Err(ReplacementSequenceError::UnsatisfiedPrecondition(
                        "every published target must have a durable rollback stage before restore",
                    ));
                }
                let index = self.target_index(target)?;
                if !self.targets[index].published || !self.targets[index].rollback_staged {
                    return Err(ReplacementSequenceError::UnsatisfiedPrecondition(
                        "restore requires a published target and its rollback stage",
                    ));
                }
                if self.targets[index].restored {
                    return Err(ReplacementSequenceError::DuplicateTransition(
                        target.clone(),
                    ));
                }
                self.targets[index].restored = true;
                Ok(())
            }
            ReplacementEvent::Complete => {
                self.require_phase(ReplacementPhase::CommitPrepared)?;
                if !self.targets.iter().all(|progress| progress.verified) {
                    return Err(ReplacementSequenceError::UnsatisfiedPrecondition(
                        "every target must be verified before completion",
                    ));
                }
                self.phase = ReplacementPhase::Complete;
                Ok(())
            }
            ReplacementEvent::RolledBack => {
                self.require_phase(ReplacementPhase::RollbackPrepared)?;
                let has_published = self.targets.iter().any(|progress| progress.published);
                let all_published_restored = self
                    .targets
                    .iter()
                    .filter(|progress| progress.published)
                    .all(|progress| progress.restored);
                if !has_published || !all_published_restored {
                    return Err(ReplacementSequenceError::UnsatisfiedPrecondition(
                        "every published target must be restored before rollback completion",
                    ));
                }
                self.phase = ReplacementPhase::RolledBack;
                Ok(())
            }
        }
    }

    fn require_phase(&self, phase: ReplacementPhase) -> Result<(), ReplacementSequenceError> {
        if self.phase == phase {
            Ok(())
        } else {
            Err(ReplacementSequenceError::InvalidTransition(self.phase))
        }
    }

    fn target_index(&self, target: &str) -> Result<usize, ReplacementSequenceError> {
        self.targets
            .binary_search_by(|progress| progress.name.as_str().cmp(target))
            .map_err(|_| ReplacementSequenceError::UnknownTarget(target.to_owned()))
    }
}

fn validate_targets(targets: &[String]) -> Result<(), ReplacementSequenceError> {
    if targets.is_empty()
        || targets.len() > MAX_TARGETS
        || targets.iter().any(|target| {
            target.is_empty()
                || target.len() > MAX_TARGET_NAME_BYTES
                || target == "."
                || target == ".."
                || target.contains('/')
                || target.contains('\\')
                || target.contains('\0')
        })
        || targets
            .windows(2)
            .any(|pair| pair[0].as_str() >= pair[1].as_str())
    {
        return Err(ReplacementSequenceError::InvalidTargets);
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    fn target(name: &str) -> String {
        name.to_owned()
    }

    #[test]
    fn complete_requires_every_target_to_follow_the_commit_order() {
        let events = vec![
            ReplacementEvent::Begin {
                targets: vec![target("a.json"), target("b.json")],
            },
            ReplacementEvent::BackedUp {
                target: target("b.json"),
            },
            ReplacementEvent::BackedUp {
                target: target("a.json"),
            },
            ReplacementEvent::Staged {
                target: target("a.json"),
            },
            ReplacementEvent::Staged {
                target: target("b.json"),
            },
            ReplacementEvent::CommitPrepared,
            ReplacementEvent::Published {
                target: target("a.json"),
            },
            ReplacementEvent::Verified {
                target: target("a.json"),
            },
            ReplacementEvent::Published {
                target: target("b.json"),
            },
            ReplacementEvent::Verified {
                target: target("b.json"),
            },
            ReplacementEvent::Complete,
        ];

        let sequence = ReplacementSequence::replay(&events).unwrap();
        assert_eq!(sequence.phase(), ReplacementPhase::Complete);
        assert_eq!(sequence.targets().collect::<Vec<_>>(), ["a.json", "b.json"]);
    }

    #[test]
    fn desired_stage_is_rejected_until_every_preimage_is_backed_up() {
        let mut sequence =
            ReplacementSequence::begin(vec![target("a.json"), target("b.json")]).unwrap();
        sequence
            .apply(&ReplacementEvent::BackedUp {
                target: target("a.json"),
            })
            .unwrap();

        assert!(matches!(
            sequence.apply(&ReplacementEvent::Staged {
                target: target("a.json")
            }),
            Err(ReplacementSequenceError::UnsatisfiedPrecondition(_))
        ));
        assert_eq!(sequence.phase(), ReplacementPhase::Preparing);
    }

    #[test]
    fn every_rollback_stage_precedes_the_first_restore() {
        let mut sequence = ReplacementSequence::replay(&[
            ReplacementEvent::Begin {
                targets: vec![target("a.json"), target("b.json")],
            },
            ReplacementEvent::BackedUp {
                target: target("a.json"),
            },
            ReplacementEvent::BackedUp {
                target: target("b.json"),
            },
            ReplacementEvent::Staged {
                target: target("a.json"),
            },
            ReplacementEvent::Staged {
                target: target("b.json"),
            },
            ReplacementEvent::CommitPrepared,
            ReplacementEvent::Published {
                target: target("a.json"),
            },
            ReplacementEvent::Verified {
                target: target("a.json"),
            },
            ReplacementEvent::Published {
                target: target("b.json"),
            },
            ReplacementEvent::RollbackPrepared {
                reason: RollbackReason::ValidationFailed,
            },
        ])
        .unwrap();
        sequence
            .apply(&ReplacementEvent::RollbackStaged {
                target: target("a.json"),
            })
            .unwrap();

        assert!(matches!(
            sequence.apply(&ReplacementEvent::Restored {
                target: target("a.json")
            }),
            Err(ReplacementSequenceError::UnsatisfiedPrecondition(_))
        ));

        sequence
            .apply(&ReplacementEvent::RollbackStaged {
                target: target("b.json"),
            })
            .unwrap();
        sequence
            .apply(&ReplacementEvent::Restored {
                target: target("b.json"),
            })
            .unwrap();
        sequence
            .apply(&ReplacementEvent::Restored {
                target: target("a.json"),
            })
            .unwrap();
        sequence.apply(&ReplacementEvent::RolledBack).unwrap();
        assert_eq!(sequence.phase(), ReplacementPhase::RolledBack);
        assert_eq!(
            sequence.rollback_reason(),
            Some(RollbackReason::ValidationFailed)
        );
        assert_eq!(
            sequence.target_state("a.json").unwrap(),
            ReplacementTargetState {
                backed_up: true,
                staged: true,
                published: true,
                verified: true,
                rollback_staged: true,
                restored: true,
            }
        );
    }

    #[test]
    fn rollback_cannot_replace_an_unpublished_target() {
        let events = [
            ReplacementEvent::Begin {
                targets: vec![target("a.json"), target("b.json")],
            },
            ReplacementEvent::BackedUp {
                target: target("a.json"),
            },
            ReplacementEvent::BackedUp {
                target: target("b.json"),
            },
            ReplacementEvent::Staged {
                target: target("a.json"),
            },
            ReplacementEvent::Staged {
                target: target("b.json"),
            },
            ReplacementEvent::CommitPrepared,
            ReplacementEvent::Published {
                target: target("a.json"),
            },
            ReplacementEvent::RollbackPrepared {
                reason: RollbackReason::PermanentPublicationFailure,
            },
        ];
        let mut sequence = ReplacementSequence::replay(&events).unwrap();

        assert!(matches!(
            sequence.apply(&ReplacementEvent::RollbackStaged {
                target: target("b.json")
            }),
            Err(ReplacementSequenceError::UnsatisfiedPrecondition(_))
        ));
    }

    #[test]
    fn target_set_and_terminal_state_fail_closed() {
        assert!(matches!(
            ReplacementSequence::begin(vec![target("b.json"), target("a.json")]),
            Err(ReplacementSequenceError::InvalidTargets)
        ));
        assert!(matches!(
            ReplacementSequence::begin(vec![target("../a.json")]),
            Err(ReplacementSequenceError::InvalidTargets)
        ));
        assert!(matches!(
            ReplacementSequence::begin(
                (0..=MAX_TARGETS)
                    .map(|index| format!("{index:02}.json"))
                    .collect()
            ),
            Err(ReplacementSequenceError::InvalidTargets)
        ));
        assert!(matches!(
            ReplacementSequence::begin(vec!["x".repeat(MAX_TARGET_NAME_BYTES + 1)]),
            Err(ReplacementSequenceError::InvalidTargets)
        ));

        let mut sequence = ReplacementSequence::replay(&[
            ReplacementEvent::Begin {
                targets: vec![target("a.json")],
            },
            ReplacementEvent::BackedUp {
                target: target("a.json"),
            },
            ReplacementEvent::Staged {
                target: target("a.json"),
            },
            ReplacementEvent::CommitPrepared,
            ReplacementEvent::Published {
                target: target("a.json"),
            },
            ReplacementEvent::Verified {
                target: target("a.json"),
            },
            ReplacementEvent::Complete,
        ])
        .unwrap();
        assert!(matches!(
            sequence.apply(&ReplacementEvent::Verified {
                target: target("a.json")
            }),
            Err(ReplacementSequenceError::InvalidTransition(
                ReplacementPhase::Complete
            ))
        ));
    }
}
