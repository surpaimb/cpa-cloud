/* Independent A2 injector/verifier; test-only, see CONTRACT.md / PROVENANCE.md. */
#define _GNU_SOURCE
#define SECRET_API_SUBJECT_TO_CHANGE
#include <libsecret/secret.h>
#include <fcntl.h>
#include <stdint.h>
#include <stdio.h>
#include <string.h>
#include <sys/random.h>
#include <sys/stat.h>
#include <unistd.h>

#define ENVELOPE_SIZE 148
#define KEY_SIZE 32
#define SESSION "/org/freedesktop/secrets/collection/session"
typedef struct { SecretService parent; } A2NoPrompt;
typedef struct { SecretServiceClass parent; } A2NoPromptClass;
G_DEFINE_TYPE(A2NoPrompt, a2_no_prompt, SECRET_TYPE_SERVICE)
static GVariant *refuse_prompt(SecretService *s, SecretPrompt *p, GCancellable *c,
                              const GVariantType *t, GError **e) {
    (void)s; (void)p; (void)c; (void)t;
    g_set_error_literal(e, G_IO_ERROR, G_IO_ERROR_PERMISSION_DENIED, "a2-prompt-denied");
    return NULL;
}
static void a2_no_prompt_class_init(A2NoPromptClass *c) {
    SECRET_SERVICE_CLASS(c)->prompt_sync = refuse_prompt;
}
static void a2_no_prompt_init(A2NoPrompt *s) { (void)s; }
static void wipe(void *buffer, size_t length) {
    volatile unsigned char *p = buffer;
    while (length--) *p++ = 0;
}
static gboolean private_file(const struct stat *s, size_t length) {
    return S_ISREG(s->st_mode) && (s->st_mode & 07777) == 0600 &&
        s->st_uid == geteuid() && s->st_gid == getegid() && s->st_nlink == 1 &&
        s->st_size == (off_t)length;
}
static gboolean same_file(const struct stat *a, const struct stat *b) {
    return a->st_dev == b->st_dev && a->st_ino == b->st_ino;
}
static int private_directory(void) {
    int fd = open(".", O_RDONLY | O_DIRECTORY | O_NOFOLLOW | O_CLOEXEC);
    struct stat s;
    if (fd < 0) return -1;
    if (fstat(fd, &s) || !S_ISDIR(s.st_mode) || (s.st_mode & 07777) != 0700 ||
        s.st_uid != geteuid() || s.st_gid != getegid()) { close(fd); return -1; }
    return fd;
}
static gboolean save_sample(int dir, const char *name, const void *bytes, size_t length) {
    int fd = openat(dir, name, O_WRONLY | O_CREAT | O_EXCL | O_NOFOLLOW | O_CLOEXEC, 0600);
    struct stat before, after, entry;
    if (fd < 0) return FALSE;
    /* All prewrite predicates precede the first byte. Never repair/recreate. */
    gboolean ok = fstat(fd, &before) == 0 && private_file(&before, 0);
    if (ok) ok = write(fd, bytes, length) == (ssize_t)length && fsync(fd) == 0;
    if (ok) ok = fstat(fd, &after) == 0 && private_file(&after, length) && same_file(&before, &after);
    if (ok) ok = fsync(dir) == 0 && fstatat(dir, name, &entry, AT_SYMLINK_NOFOLLOW) == 0 &&
        private_file(&entry, length) && same_file(&before, &entry);
    if (close(fd)) ok = FALSE;
    return ok;
}
static gboolean read_sample(int dir, const char *name, void *bytes, size_t length) {
    int fd = openat(dir, name, O_RDONLY | O_NOFOLLOW | O_CLOEXEC);
    struct stat before, after, entry;
    unsigned char extra;
    if (fd < 0) return FALSE;
    gboolean ok = fstat(fd, &before) == 0 && private_file(&before, length);
    if (ok) ok = read(fd, bytes, length) == (ssize_t)length && read(fd, &extra, 1) == 0;
    if (ok) ok = fstat(fd, &after) == 0 && private_file(&after, length) && same_file(&before, &after) &&
        fstatat(dir, name, &entry, AT_SYMLINK_NOFOLLOW) == 0 && private_file(&entry, length) &&
        same_file(&before, &entry);
    if (close(fd)) ok = FALSE;
    return ok;
}
static gboolean read_path(int dir, const char *name, char out[256]) {
    struct stat s;
    if (fstatat(dir, name, &s, AT_SYMLINK_NOFOLLOW) || s.st_size <= 0 || s.st_size >= 256 ||
        !private_file(&s, (size_t)s.st_size)) return FALSE;
    size_t n = (size_t)s.st_size;
    if (!read_sample(dir, name, out, n)) return FALSE;
    out[n] = 0;
    return strlen(out) == n && g_variant_is_object_path(out) && g_str_has_prefix(out, SESSION "/");
}
static void checksum(const unsigned char *bytes, unsigned char out[32]) {
    static const unsigned char context[] = "cpa-cloud/linux-secret-service/envelope/v1";
    GChecksum *sum = g_checksum_new(G_CHECKSUM_SHA256);
    g_checksum_update(sum, context, sizeof context);
    g_checksum_update(sum, bytes, 116);
    gsize length = 32;
    g_checksum_get_digest(sum, out, &length);
    g_checksum_free(sum);
}
static gboolean valid_envelope(const unsigned char *bytes, size_t length,
                               const unsigned char reference[148], const unsigned char key[32]) {
    if (length != ENVELOPE_SIZE || memcmp(bytes, "CPA-SS-KEY-V1\0\0\0", 16) ||
        memcmp(reference, "CPA-SS-REF-V1\0\0\0", 16) || memcmp(bytes + 16, reference + 16, 52) ||
        memcmp(bytes + 84, key, KEY_SIZE)) return FALSE;
    uint32_t uid = ((uint32_t)bytes[32] << 24) | ((uint32_t)bytes[33] << 16) |
        ((uint32_t)bytes[34] << 8) | bytes[35];
    if (uid != (uint32_t)geteuid()) return FALSE;
    unsigned char sum[32];
    checksum(bytes, sum);
    gboolean ok = memcmp(sum, bytes + 116, 32) == 0;
    wipe(sum, sizeof sum);
    return ok;
}
static void hex_bytes(const unsigned char *bytes, size_t length, char *out) {
    static const char digits[] = "0123456789abcdef";
    for (size_t i = 0; i < length; i++) {
        out[i * 2] = digits[bytes[i] >> 4]; out[i * 2 + 1] = digits[bytes[i] & 15];
    }
    out[length * 2] = 0;
}
static GHashTable *attributes(const unsigned char envelope[148]) {
    char txn[33], ns[65], id[33], uid[32];
    hex_bytes(envelope + 16, 16, txn); hex_bytes(envelope + 36, 32, ns);
    hex_bytes(envelope + 68, 16, id);
    g_snprintf(uid, sizeof uid, "%u", (unsigned)geteuid());
    GHashTable *attrs = g_hash_table_new_full(g_str_hash, g_str_equal, g_free, g_free);
    const char *names[] = {"application", "purpose", "format", "transaction", "unix-user", "lock-namespace", "item-id"};
    const char *values[] = {"cpa-cloud", "employee-config-recovery-key", "1", txn, uid, ns, id};
    for (unsigned i = 0; i < G_N_ELEMENTS(names); i++)
        g_hash_table_insert(attrs, g_strdup(names[i]), g_strdup(values[i]));
    return attrs;
}
static gboolean same_attributes(GHashTable *actual, GHashTable *expected) {
    if (!actual || g_hash_table_size(actual) != 7) return FALSE;
    GHashTableIter iter; gpointer name, value;
    g_hash_table_iter_init(&iter, expected);
    while (g_hash_table_iter_next(&iter, &name, &value))
        if (g_strcmp0(g_hash_table_lookup(actual, name), value)) return FALSE;
    return TRUE;
}
static gboolean inject(SecretService *service, int dir, const unsigned char reference[148],
                       const unsigned char original[148], const unsigned char original_key[32],
                       const char *original_path) {
    unsigned char second[148], key[32];
    memcpy(second, original, sizeof second);
    second[68] ^= 1; /* guaranteed different identity; base-query attributes unchanged */
    gboolean ok = getrandom(key, sizeof key, 0) == (ssize_t)sizeof key && memcmp(key, original_key, sizeof key) != 0;
    if (!ok) { wipe(key, sizeof key); wipe(second, sizeof second); return FALSE; }
    memcpy(second + 84, key, sizeof key);
    checksum(second, second + 116);
    ok = valid_envelope(second, sizeof second, reference, key) &&
        save_sample(dir, "duplicate.sent.key", key, sizeof key) &&
        save_sample(dir, "duplicate.sent.envelope", second, sizeof second);
    /* The exact outgoing bytes, not a returned value, are now durably captured. */
    if (ok) {
        SecretValue *value = secret_value_new((const char *)second, sizeof second, "application/octet-stream");
        GVariant *encoded = secret_service_encode_dbus_secret(service, value);
        GHashTable *attrs = attributes(second);
        GVariantBuilder properties, attr_values;
        g_variant_builder_init(&properties, G_VARIANT_TYPE("a{sv}"));
        g_variant_builder_init(&attr_values, G_VARIANT_TYPE("a{ss}"));
        GHashTableIter iter; gpointer name, text;
        g_hash_table_iter_init(&iter, attrs);
        while (g_hash_table_iter_next(&iter, &name, &text))
            g_variant_builder_add(&attr_values, "{ss}", (char *)name, (char *)text);
        g_variant_builder_add(&properties, "{sv}", "org.freedesktop.Secret.Item.Attributes", g_variant_builder_end(&attr_values));
        g_variant_builder_add(&properties, "{sv}", "org.freedesktop.Secret.Item.Label", g_variant_new_string("CPA synthetic A2 duplicate"));
        gchar *owner = g_dbus_proxy_get_name_owner(G_DBUS_PROXY(service));
        GVariant *reply = NULL;
        if (encoded && owner) {
            reply = g_dbus_connection_call_sync(g_dbus_proxy_get_connection(G_DBUS_PROXY(service)), owner,
                SESSION, "org.freedesktop.Secret.Collection", "CreateItem",
                g_variant_new("(a{sv}@(oayays)b)", &properties, encoded, FALSE),
                G_VARIANT_TYPE("(oo)"), G_DBUS_CALL_FLAGS_NO_AUTO_START, 5000, NULL, NULL);
        } else {
            g_variant_builder_clear(&properties);
            if (encoded) g_variant_unref(encoded);
        }
        ok = reply != NULL;
        if (reply) {
            const char *path, *prompt;
            g_variant_get(reply, "(&o&o)", &path, &prompt);
            ok = strcmp(prompt, "/") == 0 && g_str_has_prefix(path, SESSION "/") &&
                strcmp(path, original_path) != 0 && strlen(path) < 256 &&
                save_sample(dir, "duplicate.item.path", path, strlen(path));
            g_variant_unref(reply);
        }
        g_free(owner); g_hash_table_unref(attrs); secret_value_unref(value);
    }
    wipe(key, sizeof key); wipe(second, sizeof second);
    return ok;
}
static gboolean verify_pair(SecretService *service, int dir, const unsigned char reference[148],
                            const unsigned char original[148], const unsigned char original_key[32],
                            const char *original_path) {
    unsigned char second[148], key[32]; char second_path[256];
    gboolean ok = read_sample(dir, "duplicate.sent.envelope", second, sizeof second) &&
        read_sample(dir, "duplicate.sent.key", key, sizeof key) && read_path(dir, "duplicate.item.path", second_path) &&
        valid_envelope(second, sizeof second, reference, key) && memcmp(second + 68, original + 68, 16) != 0 &&
        memcmp(key, original_key, sizeof key) != 0 && strcmp(second_path, original_path) != 0;
    GHashTable *query = g_hash_table_new(g_str_hash, g_str_equal);
    g_hash_table_insert(query, "application", "cpa-cloud");
    g_hash_table_insert(query, "purpose", "employee-config-recovery-key");
    gchar **unlocked = NULL, **locked = NULL;
    if (ok) ok = secret_service_search_for_dbus_paths_sync(service, NULL, query, NULL, &unlocked, &locked, NULL);
    /* Broad query catches a third application/purpose item even with altered identity. */
    if (ok) ok = unlocked && g_strv_length(unlocked) == 2 && (!locked || g_strv_length(locked) == 0) &&
        strcmp(unlocked[0], unlocked[1]) != 0;
    for (unsigned i = 0; ok && i < 2; i++) {
        const unsigned char *expected = NULL, *expected_key = NULL;
        if (!strcmp(unlocked[i], original_path)) { expected = original; expected_key = original_key; }
        else if (!strcmp(unlocked[i], second_path)) { expected = second; expected_key = key; }
        else { ok = FALSE; break; }
        SecretItem *item = secret_item_new_for_dbus_path_sync(service, unlocked[i], SECRET_ITEM_NONE, NULL, NULL);
        GHashTable *actual = item ? secret_item_get_attributes(item) : NULL;
        GHashTable *wanted = attributes(expected);
        ok = item && !secret_item_get_locked(item) && same_attributes(actual, wanted);
        /* Exactly one necessary GetSecret per item per checkpoint; no diagnostic reread. */
        SecretValue *value = ok ? secret_service_get_secret_for_dbus_path_sync(service, unlocked[i], NULL, NULL) : NULL;
        gsize size = 0;
        const unsigned char *bytes = value ? (const unsigned char *)secret_value_get(value, &size) : NULL;
        ok = value && g_strcmp0(secret_value_get_content_type(value), "text/plain") == 0 &&
            valid_envelope(bytes, size, reference, expected_key) && memcmp(bytes, expected, ENVELOPE_SIZE) == 0;
        if (value) secret_value_unref(value);
        if (actual) g_hash_table_unref(actual);
        g_hash_table_unref(wanted);
        if (item) g_object_unref(item);
    }
    g_hash_table_unref(query); g_strfreev(unlocked); g_strfreev(locked);
    wipe(key, sizeof key); wipe(second, sizeof second);
    return ok;
}
static gboolean self_test(void) {
    /* Public dummy bytes only: no randomness, service, D-Bus or secret transport. */
    unsigned char bytes[148] = {0}, reference[148] = {0}, key[32] = {0};
    memcpy(bytes, "CPA-SS-KEY-V1\0\0\0", 16); memcpy(reference, "CPA-SS-REF-V1\0\0\0", 16);
    uint32_t uid = (uint32_t)geteuid();
    for (unsigned i = 0; i < 4; i++) bytes[32+i] = (unsigned char)(uid >> (24 - i*8));
    memcpy(reference + 16, bytes + 16, 52); checksum(bytes, bytes + 116);
    if (!valid_envelope(bytes, sizeof bytes, reference, key) || valid_envelope(bytes, 147, reference, key)) return FALSE;
    for (unsigned i = 0; i < sizeof bytes; i++) {
        bytes[i] ^= 1;
        if (valid_envelope(bytes, sizeof bytes, reference, key)) return FALSE;
        bytes[i] ^= 1;
    }
    int dir = private_directory();
    if (dir < 0) return FALSE;
    unsigned char readback[32]; struct stat s;
    gboolean ok = save_sample(dir, "public.sample", key, sizeof key) &&
        !save_sample(dir, "public.sample", "must-not-overwrite", 18) &&
        read_sample(dir, "public.sample", readback, sizeof readback) && !memcmp(readback, key, sizeof key) &&
        !read_sample(dir, "public.sample", readback, sizeof readback - 1) &&
        symlinkat("public.sample", dir, "public.alias") == 0 &&
        !save_sample(dir, "public.alias", key, sizeof key) && !read_sample(dir, "public.alias", readback, sizeof readback) &&
        fstatat(dir, "public.sample", &s, AT_SYMLINK_NOFOLLOW) == 0;
    if (ok) {
        struct stat changed = s; changed.st_mode |= 0040;
        ok = !private_file(&changed, sizeof key);
        changed = s; changed.st_nlink = 2; ok = ok && !private_file(&changed, sizeof key);
        changed = s; changed.st_uid++; ok = ok && !private_file(&changed, sizeof key);
        changed = s; changed.st_gid++; ok = ok && !private_file(&changed, sizeof key);
        changed = s; changed.st_mode = S_IFDIR | 0600; ok = ok && !private_file(&changed, sizeof key);
    }
    close(dir);
    return ok; /* caller owns the public-only temporary self-test directory */
}
int main(int argc, char **argv) {
    if (argc != 2) return 10;
    if (!strcmp(argv[1], "--self-test")) {
        if (!self_test()) return 11;
        g_print("A2_HELPER_SELF_TEST_OK\n"); return 0;
    }
    if (geteuid() == 0 || getuid() != geteuid()) return 12;
    const char *address = g_getenv("DBUS_SESSION_BUS_ADDRESS");
    if (!address || !g_str_has_prefix(address, "unix:path=/opt/cpa-ss-preflight.")) return 13;
    int dir = private_directory();
    if (dir < 0) return 14;
    unsigned char reference[148], original[148], original_key[32]; char original_path[256];
    gboolean ok = read_sample(dir, "reference.bin", reference, sizeof reference) &&
        read_sample(dir, "expected.envelope", original, sizeof original) &&
        read_sample(dir, "expected.key", original_key, sizeof original_key) && read_path(dir, "item.path", original_path) &&
        valid_envelope(original, sizeof original, reference, original_key) && memcmp(original + 68, reference + 68, 16) == 0;
    SecretService *service = ok ? secret_service_open_sync(a2_no_prompt_get_type(), "org.freedesktop.secrets",
        SECRET_SERVICE_OPEN_SESSION, NULL, NULL) : NULL;
    ok = service != NULL;
    if (ok && !strcmp(argv[1], "inject")) ok = inject(service, dir, reference, original, original_key, original_path);
    else if (ok && !strcmp(argv[1], "verify-two")) ok = verify_pair(service, dir, reference, original, original_key, original_path);
    else ok = FALSE;
    if (service) g_object_unref(service);
    close(dir); wipe(original_key, sizeof original_key); wipe(original, sizeof original); wipe(reference, sizeof reference);
    if (!ok) return 20;
    g_print("A2_HELPER_OK\n");
    return 0;
}
