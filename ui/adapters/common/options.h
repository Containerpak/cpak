/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
#ifndef CPAK_UI_OPTIONS_H
#define CPAK_UI_OPTIONS_H

#include <stdbool.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

static const char *cpak_option(int argc, char **argv, const char *name, const char *fallback) {
    for (int index = 2; index + 1 < argc; index += 2) {
        if (strcmp(argv[index], name) == 0) {
            return argv[index + 1];
        }
    }
    return fallback;
}

static bool cpak_boolean(int argc, char **argv, const char *name) {
    return strcmp(cpak_option(argc, argv, name, "false"), "true") == 0;
}

static bool cpak_valid_protocol(int argc, char **argv) {
    return strcmp(cpak_option(argc, argv, "--protocol", ""), "1") == 0;
}

static void cpak_reply(bool accepted, bool parent, bool persistent) {
    printf("%s %s %s\n", accepted ? "allow" : "deny", parent ? "true" : "false", persistent ? "true" : "false");
    fflush(stdout);
}

#endif
