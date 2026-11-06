export type ClientDetection = {
  id: string;
  name: string;
  detected: boolean;
  detail: string;
};

export type ProtocolConfiguration = {
  protocol: string;
  candidates: string[];
  default_model: string;
  default_source: string;
};

export type ClientConfiguration = {
  schema_version: number;
  protocols: ProtocolConfiguration[];
};

export type AppPhase = "idle" | "checking" | "ready" | "applying" | "blocked";
