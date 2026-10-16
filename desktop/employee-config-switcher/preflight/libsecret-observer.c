/* Independently authored metadata-only observer; see CONTRACT.md.
 * No Secret Service session, collection/item operation, secret or prompt API.
 * Private bus policy also denies every Secret.* interface and Properties.Set.
 */
#include <libsecret/secret.h>
#include <stdlib.h>
#include <unistd.h>

static gboolean identity(GDBusConnection *bus, const char *name,
                         guint expected_pid, guint expected_uid) {
    const char *methods[] = {"GetConnectionUnixProcessID", "GetConnectionUnixUser"};
    guint expected[] = {expected_pid, expected_uid};
    for (unsigned i = 0; i < 2; i++) {
        GVariant *reply = g_dbus_connection_call_sync(
            bus, "org.freedesktop.DBus", "/org/freedesktop/DBus",
            "org.freedesktop.DBus", methods[i], g_variant_new("(s)", name),
            G_VARIANT_TYPE("(u)"), G_DBUS_CALL_FLAGS_NO_AUTO_START,
            2000, NULL, NULL);
        if (!reply) return FALSE;
        guint actual = 0;
        g_variant_get(reply, "(u)", &actual);
        g_variant_unref(reply);
        if (actual != expected[i]) return FALSE;
    }
    return TRUE;
}

static guint argument(const char *value) {
    char *end = NULL;
    unsigned long number = strtoul(value, &end, 10);
    return end && *end == '\0' && number > 0 && number <= G_MAXUINT ? (guint)number : 0;
}

int main(int argc, char **argv) {
    if (argc != 3 || getuid() == 0) return 10;
    guint bus_pid = argument(argv[1]), service_pid = argument(argv[2]);
    if (!bus_pid || !service_pid || bus_pid == service_pid) return 10;
    const char *address = g_getenv("DBUS_SESSION_BUS_ADDRESS");
    if (!address || !g_str_has_prefix(address, "unix:path=")) return 10;
    GDBusConnection *bus = g_dbus_connection_new_for_address_sync(
        address, G_DBUS_CONNECTION_FLAGS_AUTHENTICATION_CLIENT |
                 G_DBUS_CONNECTION_FLAGS_MESSAGE_BUS_CONNECTION,
        NULL, NULL, NULL);
    if (!bus) return 20;
    if (!identity(bus, "org.freedesktop.DBus", bus_pid, getuid())) return 21;
    if (!identity(bus, "org.freedesktop.secrets", service_pid, getuid())) return 30;
    /* NONE does not request a secret-transfer session or load collections. */
    SecretService *service = secret_service_get_sync(SECRET_SERVICE_NONE, NULL, NULL);
    if (!service || secret_service_get_flags(service) != SECRET_SERVICE_NONE) return 40;
    gchar *owner = g_dbus_proxy_get_name_owner(G_DBUS_PROXY(service));
    gboolean owned = owner && identity(bus, owner, service_pid, getuid());
    g_free(owner);
    g_object_unref(service);
    secret_service_disconnect();
    g_object_unref(bus);
    if (!owned) return 41;
    g_print("PREFLIGHT_LIBSECRET_OK\n");
    return 0;
}
