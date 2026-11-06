/* Independently authored test-only libsecret item-path writer. See ../interop-a2/PROVENANCE.md. */
#ifndef CPA_INTEROP_PRIVATE_PATH_H
#define CPA_INTEROP_PRIVATE_PATH_H
#include <fcntl.h>
#include <stdbool.h>
#include <stddef.h>
#include <sys/stat.h>
#include <unistd.h>

static bool item_path_metadata(const struct stat *s, size_t length) {
    return S_ISREG(s->st_mode) && (s->st_mode & 07777) == 0600 &&
        s->st_uid == geteuid() && s->st_gid == getegid() && s->st_nlink == 1 && s->st_size == (off_t)length;
}
static bool write_item_path(const char *bytes, size_t length) {
    if (!length || length >= 256) return false;
    int dir = open(".", O_RDONLY | O_DIRECTORY | O_NOFOLLOW | O_CLOEXEC);
    struct stat directory, before, after, entry;
    if (dir < 0) return false;
    if (fstat(dir, &directory) || !S_ISDIR(directory.st_mode) || (directory.st_mode & 07777) != 0700 ||
        directory.st_uid != geteuid() || directory.st_gid != getegid()) { close(dir); return false; }
    int fd = openat(dir, "item.path", O_WRONLY | O_CREAT | O_EXCL | O_NOFOLLOW | O_CLOEXEC, 0600);
    if (fd < 0) { close(dir); return false; }
    /* No bytes before private/exclusive-file checks; never repair or recreate. */
    bool ok = fstat(fd, &before) == 0 && item_path_metadata(&before, 0);
    if (ok) ok = write(fd, bytes, length) == (ssize_t)length && fsync(fd) == 0;
    if (ok) ok = fstat(fd, &after) == 0 && item_path_metadata(&after, length) &&
        before.st_dev == after.st_dev && before.st_ino == after.st_ino;
    if (ok) ok = fsync(dir) == 0 && fstatat(dir, "item.path", &entry, AT_SYMLINK_NOFOLLOW) == 0 &&
        item_path_metadata(&entry, length) && before.st_dev == entry.st_dev && before.st_ino == entry.st_ino;
    if (close(fd)) ok = false;
    if (close(dir)) ok = false;
    return ok;
}
#endif
