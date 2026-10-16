/* Independently authored B0 bus-metadata probe; see PROVENANCE.md.
 * No service calls, activation, password/control protocol or secret access.
 * Output is captured by the harness, never relayed to CI logs.
 */
#include <gio/gio.h>
#include <stdlib.h>
#include <unistd.h>

static GVariant *query(GDBusConnection *bus, const char *method,
                       const char *name, const char *signature) {
    return g_dbus_connection_call_sync(
        bus, "org.freedesktop.DBus", "/org/freedesktop/DBus",
        "org.freedesktop.DBus", method, g_variant_new("(s)", name),
        G_VARIANT_TYPE(signature), G_DBUS_CALL_FLAGS_NO_AUTO_START,
        1500, NULL, NULL);
}

static gboolean identity(GDBusConnection *bus, const char *name, guint pid) {
    const char *methods[] = {"GetConnectionUnixProcessID", "GetConnectionUnixUser"};
    guint values[] = {pid, getuid()};
    for (unsigned i = 0; i < 2; ++i) {
        GVariant *reply = query(bus, methods[i], name, "(u)");
        if (!reply) return FALSE;
        guint value;
        g_variant_get(reply, "(u)", &value);
        g_variant_unref(reply);
        if (value != values[i]) return FALSE;
    }
    return TRUE;
}

static gboolean absent(GDBusConnection *bus, const char *name) {
    GVariant *reply = query(bus, "NameHasOwner", name, "(b)");
    if (!reply) return FALSE;
    gboolean has_owner;
    g_variant_get(reply, "(b)", &has_owner);
    g_variant_unref(reply);
    return !has_owner;
}

static guint number(const char *text) {
    char *end;
    unsigned long value = strtoul(text, &end, 10);
    return *text && !*end && value > 0 && value <= G_MAXUINT ? (guint)value : 0;
}

int main(int argc, char **argv) {
    if (argc != 4 || getuid() == 0) return 10;
    const char *address = g_getenv("DBUS_SESSION_BUS_ADDRESS");
    guint bus_pid = number(argv[2]);
    if (!address || !g_str_has_prefix(address, "unix:path=") || !bus_pid) return 10;
    GDBusConnection *bus = g_dbus_connection_new_for_address_sync(
        address, G_DBUS_CONNECTION_FLAGS_AUTHENTICATION_CLIENT |
                 G_DBUS_CONNECTION_FLAGS_MESSAGE_BUS_CONNECTION, NULL, NULL, NULL);
    if (!bus || !identity(bus, "org.freedesktop.DBus", bus_pid)) return 20;
    if (g_str_equal(argv[1], "absent")) {
        if (!g_dbus_is_unique_name(argv[3]) ||
            !absent(bus, "org.freedesktop.secrets") || !absent(bus, argv[3])) return 31;
        g_print("ABSENT\n");
    } else if (g_str_equal(argv[1], "owner")) {
        guint service_pid = number(argv[3]);
        if (!service_pid || service_pid == bus_pid) return 10;
        GVariant *reply = query(bus, "GetNameOwner", "org.freedesktop.secrets", "(s)");
        if (!reply) return 30;
        const gchar *owner;
        g_variant_get(reply, "(&s)", &owner);
        if (!g_dbus_is_unique_name(owner) || !identity(bus, owner, service_pid) ||
            !identity(bus, "org.freedesktop.secrets", service_pid)) return 32;
        g_print("%s\n", owner);
        g_variant_unref(reply);
    } else {
        return 10;
    }
    g_object_unref(bus);
    return 0;
}
