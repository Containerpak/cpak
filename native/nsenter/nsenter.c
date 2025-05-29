/*
 * Copyright (c) 2025 Mirko Brombin
 *
 * Licensed under the Apache License, Version 2.0 (the “License”);
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an “AS IS” BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */
#define _GNU_SOURCE
#include <sched.h>
#include <fcntl.h>
#include <unistd.h>
#include <sys/types.h>
#include <sys/stat.h>
#include <sys/wait.h>
#include <getopt.h>
#include <stdlib.h>
#include <stdio.h>
#include <string.h>
#include <errno.h>
#include <grp.h>

#define CLONE_NEWUSER 0x10000000
#define CLONE_NEWIPC 0x08000000
#define CLONE_NEWUTS 0x04000000
#define CLONE_NEWNET 0x40000000
#define CLONE_NEWPID 0x20000000
#define CLONE_NEWNS 0x00020000

static int open_ns(int pid, const char *ns, const char *path)
{
    if (path)
        return open(path, O_RDONLY);
    char buf[64];
    snprintf(buf, sizeof(buf), "/proc/%d/ns/%s", pid, ns);
    return open(buf, O_RDONLY);
}

int main(int argc, char **argv)
{
    int pid = 0, uid = 0, gid = 0;
    int preserve = 0, nofork = 0;
    char *root = NULL, *work = NULL;
    int opt;
    struct option long_opts[] = {
        {"target", required_argument, NULL, 't'},
        {"mount", no_argument, NULL, 'm'},
        {"uts", no_argument, NULL, 'u'},
        {"ipc", no_argument, NULL, 'i'},
        {"net", no_argument, NULL, 'n'},
        {"pid", no_argument, NULL, 'p'},
        {"user", no_argument, NULL, 'U'},
        {"setuid", required_argument, NULL, 'S'},
        {"setgid", required_argument, NULL, 'G'},
        {"root", required_argument, NULL, 'r'},
        {"wd", required_argument, NULL, 'w'},
        {"preserve-credentials", no_argument, &preserve, 1},
        {"no-fork", no_argument, &nofork, 1},
        {0, 0, NULL, 0}};
    const char *short_opts = "t:muinpUS:G:r:w:F";
    int ns_enable[6] = {0};
    const int ns_flags[6] = {CLONE_NEWUSER, CLONE_NEWIPC, CLONE_NEWUTS, CLONE_NEWNET, CLONE_NEWPID, CLONE_NEWNS};
    const char *ns_names[6] = {"user", "ipc", "uts", "net", "pid", "mnt"};

    while ((opt = getopt_long(argc, argv, short_opts, long_opts, NULL)) != -1)
    {
        switch (opt)
        {
        case 't':
            pid = atoi(optarg);
            break;
        case 'm':
            ns_enable[5] = 1;
            break;
        case 'u':
            ns_enable[2] = 1;
            break;
        case 'i':
            ns_enable[1] = 1;
            break;
        case 'n':
            ns_enable[3] = 1;
            break;
        case 'p':
            ns_enable[4] = 1;
            break;
        case 'U':
            ns_enable[0] = 1;
            break;
        case 'S':
            uid = atoi(optarg);
            break;
        case 'G':
            gid = atoi(optarg);
            break;
        case 'r':
            root = optarg;
            break;
        case 'w':
            work = optarg;
            break;
        case 'F':
            nofork = 1;
            break;
        case 0:
            break;
        case '?':
        default:
            fprintf(stderr, "usage: nsenter -t PID [flags] -- prog args\n");
            exit(1);
        }
    }
    if (!pid || optind >= argc)
    {
        fprintf(stderr, "usage: nsenter -t PID [flags] -- prog args\n");
        return 1;
    }
    char **cmd = argv + optind;
    if (strcmp(cmd[0], "--") == 0)
        cmd++;

    for (int i = 0; i < 6; i++)
    {
        if (!ns_enable[i])
            continue;
        if (i == 0 && !preserve && geteuid() != 0)
            continue;
        int fd = open_ns(pid, ns_names[i], NULL);
        if (fd < 0)
        {
            perror("open_ns");
            exit(1);
        }
        if (setns(fd, ns_flags[i]) < 0)
        {
            perror("setns");
            exit(1);
        }
        close(fd);
    }
    if (root)
    {
        int fd = open_ns(pid, "root", root);
        if (fd < 0)
        {
            perror("open root");
            exit(1);
        }
        if (fchdir(fd) < 0)
        {
            perror("fchdir");
            exit(1);
        }
        if (chroot(".") < 0)
        {
            perror("chroot");
            exit(1);
        }
        close(fd);
    }
    if (work)
    {
        if (chdir(work) < 0)
        {
            perror("chdir");
            exit(1);
        }
    }
    if (!nofork && ns_enable[4])
    {
        pid_t c = fork();
        if (c < 0)
        {
            perror("fork");
            exit(1);
        }
        if (c > 0)
        {
            waitpid(c, NULL, 0);
            return 0;
        }
    }
    if (!preserve)
    {
        if (setgroups(0, NULL) < 0)
            perror("setgroups");
        if (gid)
            setgid(gid);
        if (uid)
            setuid(uid);
    }
    execvp(cmd[0], cmd);
    perror("exec");
    return 1;
}
