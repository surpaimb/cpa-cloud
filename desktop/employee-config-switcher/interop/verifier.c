/* Independently authored libsecret oracle, not linked into CPA. See CONTRACT.md. */
#define _GNU_SOURCE
#define SECRET_API_SUBJECT_TO_CHANGE
#include <libsecret/secret.h>
#include <stdint.h>
#include <stdio.h>
#include <string.h>
#include <unistd.h>
#include "private-envelope.h"

typedef struct { SecretService parent; } NoPrompt;
typedef struct { SecretServiceClass parent; } NoPromptClass;
G_DEFINE_TYPE(NoPrompt, no_prompt, SECRET_TYPE_SERVICE)
static GVariant *refuse_prompt(SecretService *s, SecretPrompt *p, GCancellable *c,
                              const GVariantType *t, GError **e) {
    (void)s; (void)p; (void)c; (void)t;
    g_set_error_literal(e, G_IO_ERROR, G_IO_ERROR_PERMISSION_DENIED, "fixture-prompt-denied");
    return NULL;
}
static void no_prompt_class_init(NoPromptClass *klass) {
    SECRET_SERVICE_CLASS(klass)->prompt_sync = refuse_prompt;
}
static void no_prompt_init(NoPrompt *self) { (void)self; }

static gboolean read_exact(const char *name, unsigned char *bytes, size_t size) {
    FILE *file = fopen(name, "rb");
    if (!file) return FALSE;
    gboolean ok = fread(bytes, 1, size, file) == size && fgetc(file) == EOF;
    fclose(file);
    return ok;
}
static void hex_bytes(const unsigned char *input, size_t size, char *out) {
    static const char digits[] = "0123456789abcdef";
    for (size_t i = 0; i < size; i++) {
        out[2*i] = digits[input[i] >> 4]; out[2*i+1] = digits[input[i] & 15];
    }
    out[2*size] = '\0';
}
static gboolean attr(GHashTable *attrs, const char *name, const char *expected) {
    return g_strcmp0(g_hash_table_lookup(attrs, name), expected) == 0;
}

int main(int argc, char **argv) {
    if (argc != 2 || getuid() == 0) return 10;
    const char *address = g_getenv("DBUS_SESSION_BUS_ADDRESS");
    if (!address || !g_str_has_prefix(address, "unix:path=/opt/cpa-ss-preflight.")) return 10;
    SecretService *service = secret_service_open_sync(no_prompt_get_type(), "org.freedesktop.secrets",
        SECRET_SERVICE_OPEN_SESSION, NULL, NULL);
    if (!service) return 20;
    if (strcmp(argv[1], "setup") == 0) {
        /* Use the real implementation's volatile collection, never create/unlock one. */
        gchar *session = secret_service_read_alias_dbus_path_sync(service, "session", NULL, NULL);
        if (!session || strcmp(session, "/org/freedesktop/secrets/collection/session") != 0) return 21;
        gboolean ok = secret_service_set_alias_to_dbus_path_sync(service, "default", session, NULL, NULL);
        g_free(session);
        if (!ok) return 22;
    } else if (strcmp(argv[1], "verify") == 0) {
        unsigned char reference[148], expected[32];
        if (!read_exact("reference.bin", reference, sizeof reference) ||
            !read_exact("expected.key", expected, sizeof expected)) return 30;
        GHashTable *query = g_hash_table_new(g_str_hash, g_str_equal);
        g_hash_table_insert(query, "application", "cpa-cloud");
        g_hash_table_insert(query, "purpose", "employee-config-recovery-key");
        gchar **unlocked = NULL, **locked = NULL;
        if (!secret_service_search_for_dbus_paths_sync(service, NULL, query, NULL, &unlocked, &locked, NULL)) return 31;
        if (!unlocked || g_strv_length(unlocked) != 1 || (locked && g_strv_length(locked) != 0)) return 32;
        if (!g_str_has_prefix(unlocked[0], "/org/freedesktop/secrets/collection/session/")) return 33;
        gchar *original = NULL;
        gsize original_size = 0;
        if (g_file_test("item.path", G_FILE_TEST_EXISTS)) {
            if (!g_file_get_contents("item.path", &original, &original_size, NULL) ||
                original_size != strlen(unlocked[0]) || memcmp(original, unlocked[0], original_size)) return 34;
            g_free(original);
        } else {
            FILE *record = fopen("item.path", "wx");
            if (!record || fwrite(unlocked[0], 1, strlen(unlocked[0]), record) != strlen(unlocked[0]) || fclose(record)) return 35;
        }
        SecretItem *item = secret_item_new_for_dbus_path_sync(service, unlocked[0], SECRET_ITEM_NONE, NULL, NULL);
        if (!item || secret_item_get_locked(item)) return 36;
        GHashTable *attrs = secret_item_get_attributes(item);
        char transaction[33], ns[65], item_id[33], uid[32];
        hex_bytes(reference + 16, 16, transaction);
        hex_bytes(reference + 36, 32, ns);
        hex_bytes(reference + 68, 16, item_id);
        g_snprintf(uid, sizeof uid, "%u", (unsigned)getuid());
        if (!attrs || g_hash_table_size(attrs) != 7 || !attr(attrs, "application", "cpa-cloud") ||
            !attr(attrs, "purpose", "employee-config-recovery-key") || !attr(attrs, "format", "1") ||
            !attr(attrs, "transaction", transaction) || !attr(attrs, "unix-user", uid) ||
            !attr(attrs, "lock-namespace", ns) || !attr(attrs, "item-id", item_id)) return 37;
        SecretValue *value = secret_service_get_secret_for_dbus_path_sync(service, unlocked[0], NULL, NULL);
        /* Independent pinned GNOME observation, not CPA's read allowlist.
         * This MIME label never permits text conversion of the binary value. */
        if (!value || g_strcmp0(secret_value_get_content_type(value), "text/plain")) return 38;
        gsize size = 0;
        const unsigned char *bytes = (const unsigned char *)secret_value_get(value, &size);
        if (size != 148 || memcmp(bytes, "CPA-SS-KEY-V1\0\0\0", 16) ||
            memcmp(bytes + 16, reference + 16, 68) || memcmp(bytes + 84, expected, 32)) return 39;
        GChecksum *checksum = g_checksum_new(G_CHECKSUM_SHA256);
        static const unsigned char context[] = "cpa-cloud/linux-secret-service/envelope/v1";
        g_checksum_update(checksum, context, sizeof context);
        g_checksum_update(checksum, bytes, 116);
        unsigned char digest[32]; gsize digest_size = sizeof digest;
        g_checksum_get_digest(checksum, digest, &digest_size);
        if (digest_size != 32 || memcmp(digest, bytes + 116, 32)) return 40;
        g_checksum_free(checksum);
        /* Independent repeat-verification sample, not the pre-send scanner oracle. */
        if (!g_file_test("expected.envelope", G_FILE_TEST_EXISTS)) {
            if (!write_expected_envelope(bytes, size)) return 41;
        } else {
            unsigned char prior[148];
            if (!read_exact("expected.envelope", prior, sizeof prior) || memcmp(prior, bytes, size)) return 42;
            memset(prior, 0, sizeof prior);
        }
        memset(expected, 0, sizeof expected);
        secret_value_unref(value);
        g_hash_table_unref(attrs); g_hash_table_unref(query);
        g_object_unref(item); g_strfreev(unlocked); g_strfreev(locked);
    } else return 11;
    g_object_unref(service);
    g_print("LIBSECRET_FIXTURE_OK\n");
    return 0;
}
