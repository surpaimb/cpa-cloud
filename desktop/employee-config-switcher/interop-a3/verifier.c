/* CPA-owned independent A3 verifier. Reuses current volatile oracle utilities,
 * not a scanner oracle and never a deleter. See PROVENANCE.md. */
#define main volatile_oracle_main
#include "../interop/verifier.c"
#undef main
#include <fcntl.h>
#include <sys/stat.h>

static gboolean a3_read(const char *name, unsigned char *out, size_t cap, size_t *length) {
    int fd = open(name, O_RDONLY | O_NOFOLLOW | O_CLOEXEC);
    if (fd < 0) return FALSE;
    struct stat a, b;
    gboolean ok = fstat(fd, &a) == 0 && S_ISREG(a.st_mode) && a.st_uid == getuid()
        && a.st_gid == getgid() && (a.st_mode & 07777) == 0600 && a.st_nlink == 1
        && a.st_size > 0 && (uint64_t)a.st_size <= cap;
    size_t size = ok ? (size_t)a.st_size : 0;
    size_t done = 0;
    while (ok && done < size) {
        ssize_t n = read(fd, out + done, size - done);
        if (n <= 0) ok = FALSE; else done += (size_t)n;
    }
    unsigned char extra;
    ok = ok && read(fd, &extra, 1) == 0 && fstat(fd, &b) == 0
        && a.st_dev == b.st_dev && a.st_ino == b.st_ino && a.st_size == b.st_size
        && a.st_mode == b.st_mode && b.st_nlink == 1 && a.st_uid == b.st_uid && a.st_gid == b.st_gid;
    if (close(fd) != 0) ok = FALSE;
    *length = done;
    return ok;
}

static gboolean checksum_ok(const unsigned char *bytes, const char *context, size_t context_len) {
    GChecksum *sum = g_checksum_new(G_CHECKSUM_SHA256);
    unsigned char digest[32]; gsize n = sizeof digest;
    g_checksum_update(sum, (const guchar *)context, context_len);
    g_checksum_update(sum, bytes, 116);
    g_checksum_get_digest(sum, digest, &n);
    gboolean ok = n == 32 && memcmp(digest, bytes + 116, 32) == 0;
    g_checksum_free(sum);
    return ok;
}

static gboolean denied_probe(SecretService *s, const char *destination, const char *interface, const char *member) {
    GDBusMessage *message = g_dbus_message_new_method_call(destination,
        "/org/freedesktop/secrets/a3_policy_probe", interface, member);
    g_dbus_message_set_flags(message, G_DBUS_MESSAGE_FLAGS_NO_AUTO_START);
    g_dbus_message_set_body(message, g_variant_new("()"));
    GDBusMessage *reply = g_dbus_connection_send_message_with_reply_sync(
        g_dbus_proxy_get_connection(G_DBUS_PROXY(s)), message, G_DBUS_SEND_MESSAGE_FLAGS_NONE,
        5000, NULL, NULL, NULL);
    gboolean ok = reply && g_dbus_message_get_message_type(reply) == G_DBUS_MESSAGE_TYPE_ERROR
        && g_strcmp0(g_dbus_message_get_error_name(reply), "org.freedesktop.DBus.Error.AccessDenied") == 0;
    if (reply) g_object_unref(reply);
    g_object_unref(message);
    return ok;
}

static gboolean policy(SecretService *s) {
    /* Nonexistent fixture-only path: the bus must reject these headers before
     * dispatch. No prompt/unlock/collection/item method is executed. */
    return denied_probe(s, "org.freedesktop.secrets", NULL, "Delete")
        && denied_probe(s, "org.freedesktop.secrets", "org.freedesktop.Secret.Collection", "Delete")
        && denied_probe(s, "org.freedesktop.DBus", "org.freedesktop.Secret.Item", "Delete")
        && denied_probe(s, "org.freedesktop.secrets", "org.freedesktop.Secret.Prompt", "Prompt")
        && denied_probe(s, "org.freedesktop.secrets", NULL, "Unlock")
        && denied_probe(s, "org.freedesktop.secrets", NULL, "SetSecret")
        && denied_probe(s, "org.freedesktop.secrets", "org.freedesktop.DBus.Properties", "Set");
}

static gboolean path_absent(SecretService *s, const char *path) {
    GVariant *reply = g_dbus_connection_call_sync(g_dbus_proxy_get_connection(G_DBUS_PROXY(s)),
        "org.freedesktop.secrets", "/org/freedesktop/secrets/collection/session",
        "org.freedesktop.DBus.Properties", "Get",
        g_variant_new("(ss)", "org.freedesktop.Secret.Collection", "Items"),
        G_VARIANT_TYPE("(v)"), G_DBUS_CALL_FLAGS_NO_AUTO_START, 5000, NULL, NULL);
    if (!reply) return FALSE;
    GVariant *boxed = g_variant_get_child_value(reply, 0);
    GVariant *paths = g_variant_get_variant(boxed);
    gboolean ok = g_variant_is_of_type(paths, G_VARIANT_TYPE("ao"));
    if (ok) {
        GVariantIter iterator; const gchar *current;
        g_variant_iter_init(&iterator, paths);
        while (g_variant_iter_next(&iterator, "&o", &current)) if (strcmp(path, current) == 0) ok = FALSE;
    }
    g_variant_unref(paths); g_variant_unref(boxed); g_variant_unref(reply);
    return ok;
}

int main(int argc, char **argv) {
    if (argc < 2 || getuid() == 0) return 10;
    const char *address = g_getenv("DBUS_SESSION_BUS_ADDRESS");
    if (!address || !g_str_has_prefix(address, "unix:path=/opt/cpa-ss-preflight.")) return 11;
    SecretService *service = secret_service_open_sync(no_prompt_get_type(), "org.freedesktop.secrets",
        SECRET_SERVICE_OPEN_SESSION, NULL, NULL);
    if (!service) return 20;
    if (argc == 2 && strcmp(argv[1], "policy") == 0) {
        if (!policy(service)) return 21;
    } else {
        if (argc != 3 || (strcmp(argv[2], "target") && strcmp(argv[2], "neighbor"))) return 12;
        const char *work = g_getenv("CPA_SS_FIXTURE_ROOT");
        if (!work || !g_str_has_prefix(work, "/opt/cpa-ss-preflight.")) return 13;
        gchar *role = g_build_filename(work, argv[2], NULL);
        int dir = open(role, O_DIRECTORY | O_NOFOLLOW | O_CLOEXEC);
        g_free(role);
        struct stat m;
        if (dir < 0 || fstat(dir, &m) || m.st_uid != getuid() || m.st_gid != getgid()
            || (m.st_mode & 07777) != 0700 || fchdir(dir) || close(dir)) return 14;
        unsigned char reference[148], key[32]; size_t length;
        static const char rc[] = "cpa-cloud/linux-secret-service/reference/v1";
        if (!a3_read("reference.bin", reference, sizeof reference, &length) || length != 148
            || memcmp(reference, "CPA-SS-REF-V1\0\0\0", 16) || !checksum_ok(reference, rc, sizeof rc)) return 30;
        char transaction[33], ns[65], item_id[33], uid[32];
        hex_bytes(reference + 16, 16, transaction); hex_bytes(reference + 36, 32, ns);
        hex_bytes(reference + 68, 16, item_id); g_snprintf(uid, sizeof uid, "%u", (unsigned)getuid());
        GHashTable *query = g_hash_table_new(g_str_hash, g_str_equal);
        g_hash_table_insert(query, "application", "cpa-cloud");
        g_hash_table_insert(query, "purpose", "employee-config-recovery-key");
        g_hash_table_insert(query, "format", "1"); g_hash_table_insert(query, "transaction", transaction);
        g_hash_table_insert(query, "unix-user", uid); g_hash_table_insert(query, "lock-namespace", ns);
        gchar **unlocked = NULL, **locked = NULL;
        if (!secret_service_search_for_dbus_paths_sync(service, NULL, query, NULL, &unlocked, &locked, NULL)
            || (locked && g_strv_length(locked))) return 31;
        if (strcmp(argv[1], "absent") == 0) {
            unsigned char prior_path[1025];
            if ((unlocked && g_strv_length(unlocked)) || !a3_read("item.path", prior_path, 1024, &length)) return 32;
            prior_path[length] = 0;
            if (!g_variant_is_object_path((char *)prior_path) || !path_absent(service, (char *)prior_path)) return 33;
        } else {
            if (strcmp(argv[1], "snapshot") && strcmp(argv[1], "present")) return 15;
            if (!unlocked || g_strv_length(unlocked) != 1
                || !g_str_has_prefix(unlocked[0], "/org/freedesktop/secrets/collection/session/")) return 34;
            SecretItem *item = secret_item_new_for_dbus_path_sync(service, unlocked[0], SECRET_ITEM_NONE, NULL, NULL);
            if (!item || secret_item_get_locked(item)) return 35;
            GHashTable *attrs = secret_item_get_attributes(item);
            if (!attrs || g_hash_table_size(attrs) != 7 || !attr(attrs, "application", "cpa-cloud")
                || !attr(attrs, "purpose", "employee-config-recovery-key") || !attr(attrs, "format", "1")
                || !attr(attrs, "transaction", transaction) || !attr(attrs, "unix-user", uid)
                || !attr(attrs, "lock-namespace", ns) || !attr(attrs, "item-id", item_id)) return 36;
            if (!a3_read("expected.key", key, sizeof key, &length) || length != 32) return 37;
            SecretValue *value = secret_service_get_secret_for_dbus_path_sync(service, unlocked[0], NULL, NULL);
            if (!value || g_strcmp0(secret_value_get_content_type(value), "text/plain")) return 38;
            gsize size; const unsigned char *bytes = (const unsigned char *)secret_value_get(value, &size);
            static const char ec[] = "cpa-cloud/linux-secret-service/envelope/v1";
            if (size != 148 || memcmp(bytes, "CPA-SS-KEY-V1\0\0\0", 16)
                || memcmp(bytes + 16, reference + 16, 68) || memcmp(bytes + 84, key, 32)
                || !checksum_ok(bytes, ec, sizeof ec)) return 39;
            if (strcmp(argv[1], "snapshot") == 0) {
                if (!write_item_path(unlocked[0], strlen(unlocked[0])) || !write_expected_envelope(bytes, size)) return 40;
            } else {
                unsigned char path[1025], envelope[148];
                if (!a3_read("item.path", path, 1024, &length) || length != strlen(unlocked[0])
                    || memcmp(path, unlocked[0], length) || !a3_read("expected.envelope", envelope, 148, &length)
                    || length != 148 || memcmp(envelope, bytes, 148)) return 41;
                memset(envelope, 0, sizeof envelope);
            }
            memset(key, 0, sizeof key); secret_value_unref(value);
            g_hash_table_unref(attrs); g_object_unref(item);
        }
        g_hash_table_unref(query); g_strfreev(unlocked); g_strfreev(locked);
    }
    g_object_unref(service);
    g_print("A3_LIBSECRET_OK\n");
    return 0;
}
