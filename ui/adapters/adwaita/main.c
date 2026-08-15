/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
#include <adwaita.h>
#include <unistd.h>

#include "../common/options.h"

typedef struct {
    GMainLoop *loop;
    bool accepted;
    bool parent;
    bool persistent;
    GtkCheckButton *parent_button;
    GtkCheckButton *persistent_button;
} PromptState;

typedef struct {
    GMainLoop *loop;
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

static void prompt_response(AdwMessageDialog *dialog, const char *response, gpointer data) {
    (void) dialog;
    PromptState *state = data;
    state->accepted = strcmp(response, "allow") == 0;
    state->parent = state->parent_button != NULL && gtk_check_button_get_active(state->parent_button);
    state->persistent = state->persistent_button != NULL && gtk_check_button_get_active(state->persistent_button);
    g_main_loop_quit(state->loop);
}

static int prompt(int argc, char **argv) {
    adw_init();
    const char *heading = cpak_option(argc, argv, "--heading", "Confirm action");
    const char *body = cpak_option(argc, argv, "--body", "");
    const char *application = cpak_option(argc, argv, "--application", "");
    const char *resource = cpak_option(argc, argv, "--resource", "");
    const char *cancel = cpak_option(argc, argv, "--cancel-label", "Cancel");
    AdwMessageDialog *dialog = ADW_MESSAGE_DIALOG(adw_message_dialog_new(NULL, heading, body));
    gtk_window_set_title(GTK_WINDOW(dialog), cpak_option(argc, argv, "--title", "cpak"));
    if (cancel[0] != '\0') {
        adw_message_dialog_add_response(dialog, "deny", cancel);
    }
    adw_message_dialog_add_response(dialog, "allow", cpak_option(argc, argv, "--accept-label", "Continue"));
    adw_message_dialog_set_close_response(dialog, cancel[0] == '\0' ? "allow" : "deny");
    if (cpak_boolean(argc, argv, "--recommended")) {
        adw_message_dialog_set_response_appearance(dialog, "allow", ADW_RESPONSE_SUGGESTED);
        adw_message_dialog_set_default_response(dialog, "allow");
    }

    GtkWidget *box = gtk_box_new(GTK_ORIENTATION_VERTICAL, 12);
    set_margins(box, 6);
    if (application[0] != '\0') {
        char *markup = g_markup_printf_escaped("<b>%s</b> wants access to:", application);
        GtkWidget *label = gtk_label_new(NULL);
        gtk_label_set_markup(GTK_LABEL(label), markup);
        g_free(markup);
        gtk_box_append(GTK_BOX(box), label);
    }
    if (resource[0] != '\0') {
        GtkWidget *label = gtk_label_new(resource);
        gtk_label_set_wrap(GTK_LABEL(label), TRUE);
        gtk_label_set_selectable(GTK_LABEL(label), TRUE);
        gtk_box_append(GTK_BOX(box), label);
    }
    GtkWidget *parent = NULL;
    if (cpak_boolean(argc, argv, "--offer-parent")) {
        parent = gtk_check_button_new_with_label("Give access to the parent folder?");
        gtk_check_button_set_active(GTK_CHECK_BUTTON(parent), cpak_boolean(argc, argv, "--parent-selected"));
        gtk_box_append(GTK_BOX(box), parent);
    }
    GtkWidget *persistent = NULL;
    if (cpak_boolean(argc, argv, "--offer-persistent")) {
        persistent = gtk_check_button_new_with_label("Remember for this resource?");
        gtk_check_button_set_active(GTK_CHECK_BUTTON(persistent), cpak_boolean(argc, argv, "--persistent-selected"));
        gtk_box_append(GTK_BOX(box), persistent);
    }
    adw_message_dialog_set_extra_child(dialog, box);
    PromptState state = {
        .loop = g_main_loop_new(NULL, FALSE),
        .accepted = false,
        .parent_button = parent == NULL ? NULL : GTK_CHECK_BUTTON(parent),
        .persistent_button = persistent == NULL ? NULL : GTK_CHECK_BUTTON(persistent),
    };
    g_signal_connect(dialog, "response", G_CALLBACK(prompt_response), &state);
    gtk_window_present(GTK_WINDOW(dialog));
    g_main_loop_run(state.loop);
    cpak_reply(state.accepted, state.parent, state.persistent);
    g_main_loop_unref(state.loop);
    return 0;
}

static gboolean progress_input(GIOChannel *source, GIOCondition condition, gpointer data) {
    ProgressState *state = data;
    if (condition & (G_IO_HUP | G_IO_ERR | G_IO_NVAL)) {
        g_main_loop_quit(state->loop);
        return G_SOURCE_REMOVE;
    }
    gchar *line = NULL;
    if (g_io_channel_read_line(source, &line, NULL, NULL, NULL) != G_IO_STATUS_NORMAL || line == NULL) {
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
    adw_init();
    GtkWidget *window = adw_window_new();
    gtk_window_set_title(GTK_WINDOW(window), cpak_option(argc, argv, "--title", "cpak"));
    gtk_window_set_default_size(GTK_WINDOW(window), 520, 260);
    gtk_window_set_modal(GTK_WINDOW(window), TRUE);
    GtkWidget *toolbar = adw_toolbar_view_new();
    adw_toolbar_view_add_top_bar(ADW_TOOLBAR_VIEW(toolbar), adw_header_bar_new());
    GtkWidget *clamp = adw_clamp_new();
    GtkWidget *box = gtk_box_new(GTK_ORIENTATION_VERTICAL, 16);
    set_margins(box, 28);
    GtkWidget *heading = gtk_label_new(NULL);
    char *markup = g_markup_printf_escaped("<span size='x-large' weight='bold'>%s</span>", cpak_option(argc, argv, "--heading", "Working"));
    gtk_label_set_markup(GTK_LABEL(heading), markup);
    g_free(markup);
    gtk_box_append(GTK_BOX(box), heading);
    GtkWidget *body = gtk_label_new(cpak_option(argc, argv, "--body", ""));
    gtk_label_set_wrap(GTK_LABEL(body), TRUE);
    gtk_box_append(GTK_BOX(box), body);
    ProgressState state = {.loop = g_main_loop_new(NULL, FALSE)};
    state.status = gtk_label_new("");
    gtk_box_append(GTK_BOX(box), state.status);
    state.bar = gtk_progress_bar_new();
    gtk_progress_bar_set_pulse_step(GTK_PROGRESS_BAR(state.bar), 0.08);
    gtk_box_append(GTK_BOX(box), state.bar);
    adw_clamp_set_child(ADW_CLAMP(clamp), box);
    adw_toolbar_view_set_content(ADW_TOOLBAR_VIEW(toolbar), clamp);
    adw_window_set_content(ADW_WINDOW(window), toolbar);
    state.input = g_io_channel_unix_new(STDIN_FILENO);
    g_io_add_watch(state.input, G_IO_IN | G_IO_HUP | G_IO_ERR | G_IO_NVAL, progress_input, &state);
    gtk_window_present(GTK_WINDOW(window));
    g_main_loop_run(state.loop);
    g_io_channel_unref(state.input);
    g_main_loop_unref(state.loop);
    gtk_window_destroy(GTK_WINDOW(window));
    return 0;
}

int main(int argc, char **argv) {
    if (argc == 2 && strcmp(argv[1], "probe") == 0) {
        printf("cpak-ui 1 adwaita\n");
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
