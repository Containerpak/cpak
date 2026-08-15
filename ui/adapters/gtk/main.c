/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
#include <gtk/gtk.h>
#include <unistd.h>

#include "../common/options.h"

typedef struct {
    GtkWidget *window;
    GtkWidget *bar;
    GtkWidget *status;
    GIOChannel *input;
} ProgressState;

static void set_margins(GtkWidget *widget, int margin) {
    gtk_widget_set_margin_start(widget, margin);
    gtk_widget_set_margin_end(widget, margin);
    gtk_widget_set_margin_top(widget, margin);
    gtk_widget_set_margin_bottom(widget, margin);
}

static GtkWidget *wrapped_label(const char *text) {
    GtkWidget *label = gtk_label_new(text);
    gtk_label_set_line_wrap(GTK_LABEL(label), TRUE);
    gtk_label_set_xalign(GTK_LABEL(label), 0.0f);
    gtk_widget_set_halign(label, GTK_ALIGN_FILL);
    return label;
}

static int prompt(int argc, char **argv) {
    if (!gtk_init_check(&argc, &argv)) {
        return 2;
    }
    const char *title = cpak_option(argc, argv, "--title", "cpak");
    const char *heading = cpak_option(argc, argv, "--heading", "Confirm action");
    const char *body = cpak_option(argc, argv, "--body", "");
    const char *application = cpak_option(argc, argv, "--application", "");
    const char *resource = cpak_option(argc, argv, "--resource", "");
    const char *accept = cpak_option(argc, argv, "--accept-label", "Continue");
    const char *cancel = cpak_option(argc, argv, "--cancel-label", "Cancel");
    GtkWidget *dialog = gtk_dialog_new();
    gtk_window_set_title(GTK_WINDOW(dialog), title);
    gtk_window_set_modal(GTK_WINDOW(dialog), TRUE);
    gtk_window_set_destroy_with_parent(GTK_WINDOW(dialog), TRUE);
    if (cancel[0] != '\0') {
        gtk_dialog_add_button(GTK_DIALOG(dialog), cancel, GTK_RESPONSE_CANCEL);
    }
    gtk_dialog_add_button(GTK_DIALOG(dialog), accept, GTK_RESPONSE_ACCEPT);
    gtk_window_set_default_size(GTK_WINDOW(dialog), 520, -1);
    GtkWidget *content = gtk_dialog_get_content_area(GTK_DIALOG(dialog));
    gtk_box_set_spacing(GTK_BOX(content), 16);
    set_margins(content, 24);

    GtkWidget *heading_label = gtk_label_new(NULL);
    char *heading_markup = g_markup_printf_escaped("<span size='x-large' weight='bold'>%s</span>", heading);
    gtk_label_set_markup(GTK_LABEL(heading_label), heading_markup);
    g_free(heading_markup);
    gtk_widget_set_halign(heading_label, GTK_ALIGN_CENTER);
    gtk_box_pack_start(GTK_BOX(content), heading_label, FALSE, FALSE, 0);

    if (application[0] != '\0') {
        char *message = g_markup_printf_escaped("<b>%s</b> wants access to:", application);
        GtkWidget *label = gtk_label_new(NULL);
        gtk_label_set_markup(GTK_LABEL(label), message);
        g_free(message);
        gtk_widget_set_halign(label, GTK_ALIGN_CENTER);
        gtk_box_pack_start(GTK_BOX(content), label, FALSE, FALSE, 0);
    }
    if (resource[0] != '\0') {
        GtkWidget *label = wrapped_label(resource);
        gtk_label_set_selectable(GTK_LABEL(label), TRUE);
        gtk_widget_set_halign(label, GTK_ALIGN_CENTER);
        gtk_box_pack_start(GTK_BOX(content), label, FALSE, FALSE, 0);
    }
    if (body[0] != '\0') {
        gtk_box_pack_start(GTK_BOX(content), wrapped_label(body), FALSE, FALSE, 0);
    }

    GtkWidget *parent = NULL;
    if (cpak_boolean(argc, argv, "--offer-parent")) {
        parent = gtk_check_button_new_with_label("Give access to the parent folder?");
        gtk_toggle_button_set_active(GTK_TOGGLE_BUTTON(parent), cpak_boolean(argc, argv, "--parent-selected"));
        gtk_box_pack_start(GTK_BOX(content), parent, FALSE, FALSE, 0);
    }
    GtkWidget *persistent = NULL;
    if (cpak_boolean(argc, argv, "--offer-persistent")) {
        persistent = gtk_check_button_new_with_label("Remember for this resource?");
        gtk_toggle_button_set_active(GTK_TOGGLE_BUTTON(persistent), cpak_boolean(argc, argv, "--persistent-selected"));
        gtk_box_pack_start(GTK_BOX(content), persistent, FALSE, FALSE, 0);
    }
    if (cpak_boolean(argc, argv, "--recommended")) {
        GtkWidget *button = gtk_dialog_get_widget_for_response(GTK_DIALOG(dialog), GTK_RESPONSE_ACCEPT);
        gtk_style_context_add_class(gtk_widget_get_style_context(button), "suggested-action");
    }
    gtk_widget_show_all(dialog);
    int response = gtk_dialog_run(GTK_DIALOG(dialog));
    bool accepted = response == GTK_RESPONSE_ACCEPT;
    cpak_reply(accepted,
               parent != NULL && gtk_toggle_button_get_active(GTK_TOGGLE_BUTTON(parent)),
               persistent != NULL && gtk_toggle_button_get_active(GTK_TOGGLE_BUTTON(persistent)));
    gtk_widget_destroy(dialog);
    while (gtk_events_pending()) {
        gtk_main_iteration();
    }
    return 0;
}

static gboolean progress_input(GIOChannel *source, GIOCondition condition, gpointer data) {
    ProgressState *state = data;
    if (condition & (G_IO_HUP | G_IO_ERR | G_IO_NVAL)) {
        gtk_main_quit();
        return G_SOURCE_REMOVE;
    }
    gchar *line = NULL;
    gsize length = 0;
    if (g_io_channel_read_line(source, &line, &length, NULL, NULL) != G_IO_STATUS_NORMAL || line == NULL) {
        return G_SOURCE_CONTINUE;
    }
    gchar **parts = g_strsplit(line, "\t", 3);
    gint64 current = g_ascii_strtoll(parts[0], NULL, 10);
    gint64 total = parts[1] == NULL ? 0 : g_ascii_strtoll(parts[1], NULL, 10);
    if (parts[2] != NULL) {
        g_strchomp(parts[2]);
        gtk_label_set_text(GTK_LABEL(state->status), parts[2]);
    }
    if (total > 0) {
        gtk_progress_bar_set_fraction(GTK_PROGRESS_BAR(state->bar), MIN(1.0, (double) current / (double) total));
    } else {
        gtk_progress_bar_pulse(GTK_PROGRESS_BAR(state->bar));
    }
    g_strfreev(parts);
    g_free(line);
    return G_SOURCE_CONTINUE;
}

static int progress(int argc, char **argv) {
    if (!gtk_init_check(&argc, &argv)) {
        return 2;
    }
    ProgressState state = {0};
    state.window = gtk_window_new(GTK_WINDOW_TOPLEVEL);
    gtk_window_set_title(GTK_WINDOW(state.window), cpak_option(argc, argv, "--title", "cpak"));
    gtk_window_set_default_size(GTK_WINDOW(state.window), 520, 220);
    gtk_window_set_modal(GTK_WINDOW(state.window), TRUE);
    g_signal_connect(state.window, "delete-event", G_CALLBACK(gtk_widget_hide_on_delete), NULL);
    GtkWidget *box = gtk_box_new(GTK_ORIENTATION_VERTICAL, 16);
    set_margins(box, 28);
    gtk_container_add(GTK_CONTAINER(state.window), box);
    GtkWidget *heading = gtk_label_new(NULL);
    char *markup = g_markup_printf_escaped("<span size='x-large' weight='bold'>%s</span>", cpak_option(argc, argv, "--heading", "Working"));
    gtk_label_set_markup(GTK_LABEL(heading), markup);
    g_free(markup);
    gtk_box_pack_start(GTK_BOX(box), heading, FALSE, FALSE, 0);
    gtk_box_pack_start(GTK_BOX(box), wrapped_label(cpak_option(argc, argv, "--body", "")), FALSE, FALSE, 0);
    state.status = gtk_label_new("");
    gtk_box_pack_start(GTK_BOX(box), state.status, FALSE, FALSE, 0);
    state.bar = gtk_progress_bar_new();
    gtk_progress_bar_set_pulse_step(GTK_PROGRESS_BAR(state.bar), 0.08);
    gtk_box_pack_start(GTK_BOX(box), state.bar, FALSE, FALSE, 0);
    state.input = g_io_channel_unix_new(STDIN_FILENO);
    g_io_add_watch(state.input, G_IO_IN | G_IO_HUP | G_IO_ERR | G_IO_NVAL, progress_input, &state);
    gtk_widget_show_all(state.window);
    gtk_main();
    g_io_channel_unref(state.input);
    gtk_widget_destroy(state.window);
    return 0;
}

int main(int argc, char **argv) {
    if (argc == 2 && strcmp(argv[1], "probe") == 0) {
        printf("cpak-ui 1 gtk\n");
        return 0;
    }
    if (argc < 2 || !cpak_valid_protocol(argc, argv)) {
        return 2;
    }
    if (strcmp(argv[1], "prompt") == 0) {
        return prompt(argc, argv);
    }
    if (strcmp(argv[1], "progress") == 0) {
        return progress(argc, argv);
    }
    return 2;
}
