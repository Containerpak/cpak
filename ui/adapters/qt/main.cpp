/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
#include <QApplication>
#include <QByteArray>
#include <QCheckBox>
#include <QDialog>
#include <QDialogButtonBox>
#include <QLabel>
#include <QProgressBar>
#include <QPushButton>
#include <QSocketNotifier>
#include <QString>
#include <QStringList>
#include <QVBoxLayout>

#include <algorithm>
#include <cstring>
#include <iostream>
#include <unistd.h>

#ifndef CPAK_UI_BACKEND
#define CPAK_UI_BACKEND "qt"
#endif

static QString option(const QStringList &arguments, const QString &name, const QString &fallback = QString()) {
    for (int index = 2; index + 1 < arguments.size(); index += 2) {
        if (arguments[index] == name) {
            return arguments[index + 1];
        }
    }
    return fallback;
}

static bool booleanOption(const QStringList &arguments, const QString &name) {
    return option(arguments, name) == QStringLiteral("true");
}

static void reply(bool accepted, bool parent, bool persistent) {
    std::cout << (accepted ? "allow" : "deny") << ' '
              << (parent ? "true" : "false") << ' '
              << (persistent ? "true" : "false") << std::endl;
}

static int prompt(const QStringList &arguments) {
    QDialog dialog;
    dialog.setWindowTitle(option(arguments, QStringLiteral("--title"), QStringLiteral("cpak")));
    dialog.setModal(true);
    dialog.setMinimumWidth(520);
    auto *layout = new QVBoxLayout(&dialog);
    layout->setContentsMargins(28, 24, 28, 24);
    layout->setSpacing(16);

    auto *heading = new QLabel(QStringLiteral("<h2>%1</h2>").arg(option(arguments, QStringLiteral("--heading"), QStringLiteral("Confirm action")).toHtmlEscaped()));
    heading->setAlignment(Qt::AlignHCenter);
    layout->addWidget(heading);
    QString applicationName = option(arguments, QStringLiteral("--application"));
    if (!applicationName.isEmpty()) {
        auto *label = new QLabel(QStringLiteral("<b>%1</b> wants access to:").arg(applicationName.toHtmlEscaped()));
        label->setAlignment(Qt::AlignHCenter);
        layout->addWidget(label);
    }
    QString resource = option(arguments, QStringLiteral("--resource"));
    if (!resource.isEmpty()) {
        auto *label = new QLabel(resource);
        label->setTextInteractionFlags(Qt::TextSelectableByMouse);
        label->setWordWrap(true);
        label->setAlignment(Qt::AlignHCenter);
        layout->addWidget(label);
    }
    QString body = option(arguments, QStringLiteral("--body"));
    if (!body.isEmpty()) {
        auto *label = new QLabel(body);
        label->setWordWrap(true);
        layout->addWidget(label);
    }

    QCheckBox *parent = nullptr;
    if (booleanOption(arguments, QStringLiteral("--offer-parent"))) {
        parent = new QCheckBox(QStringLiteral("Give access to the parent folder?"));
        parent->setChecked(booleanOption(arguments, QStringLiteral("--parent-selected")));
        layout->addWidget(parent);
    }
    QCheckBox *persistent = nullptr;
    if (booleanOption(arguments, QStringLiteral("--offer-persistent"))) {
        persistent = new QCheckBox(QStringLiteral("Remember for this resource?"));
        persistent->setChecked(booleanOption(arguments, QStringLiteral("--persistent-selected")));
        layout->addWidget(persistent);
    }

    auto *buttons = new QDialogButtonBox;
    QString cancelLabel = option(arguments, QStringLiteral("--cancel-label"), QStringLiteral("Cancel"));
    QPushButton *cancel = nullptr;
    if (!cancelLabel.isEmpty()) {
        cancel = buttons->addButton(cancelLabel, QDialogButtonBox::RejectRole);
        QObject::connect(cancel, &QPushButton::clicked, &dialog, &QDialog::reject);
    }
    auto *accept = buttons->addButton(option(arguments, QStringLiteral("--accept-label"), QStringLiteral("Continue")), QDialogButtonBox::AcceptRole);
    if (booleanOption(arguments, QStringLiteral("--recommended"))) {
        accept->setDefault(true);
    }
    QObject::connect(accept, &QPushButton::clicked, &dialog, &QDialog::accept);
    layout->addWidget(buttons);
    bool accepted = dialog.exec() == QDialog::Accepted;
    reply(accepted, parent != nullptr && parent->isChecked(), persistent != nullptr && persistent->isChecked());
    return 0;
}

static int progress(const QStringList &arguments) {
    QDialog dialog;
    dialog.setWindowTitle(option(arguments, QStringLiteral("--title"), QStringLiteral("cpak")));
    dialog.setModal(true);
    dialog.setMinimumWidth(520);
    auto *layout = new QVBoxLayout(&dialog);
    layout->setContentsMargins(28, 24, 28, 24);
    layout->setSpacing(16);
    auto *heading = new QLabel(QStringLiteral("<h2>%1</h2>").arg(option(arguments, QStringLiteral("--heading"), QStringLiteral("Working")).toHtmlEscaped()));
    heading->setAlignment(Qt::AlignHCenter);
    layout->addWidget(heading);
    auto *body = new QLabel(option(arguments, QStringLiteral("--body")));
    body->setWordWrap(true);
    layout->addWidget(body);
    auto *status = new QLabel;
    status->setWordWrap(true);
    layout->addWidget(status);
    auto *bar = new QProgressBar;
    bar->setRange(0, 0);
    layout->addWidget(bar);

    QByteArray pending;
    QSocketNotifier notifier(STDIN_FILENO, QSocketNotifier::Read);
    QObject::connect(&notifier, &QSocketNotifier::activated, &dialog, [&](QSocketDescriptor, QSocketNotifier::Type) {
        char buffer[4096];
        ssize_t count = read(STDIN_FILENO, buffer, sizeof(buffer));
        if (count <= 0) {
            dialog.accept();
            return;
        }
        pending.append(buffer, count);
        int newline = -1;
        while ((newline = pending.indexOf('\n')) >= 0) {
            QByteArray line = pending.left(newline);
            pending.remove(0, newline + 1);
            QList<QByteArray> fields = line.split('\t');
            if (fields.size() < 3) {
                continue;
            }
            bool currentValid = false;
            bool totalValid = false;
            qint64 current = fields[0].toLongLong(&currentValid);
            qint64 total = fields[1].toLongLong(&totalValid);
            status->setText(QString::fromUtf8(fields.mid(2).join("\t")));
            if (currentValid && totalValid && total > 0) {
                bar->setRange(0, 1000);
                bar->setValue(static_cast<int>(std::min<qint64>(1000, current * 1000 / total)));
            } else {
                bar->setRange(0, 0);
            }
        }
    });
    dialog.show();
    return dialog.exec() == QDialog::Accepted ? 0 : 1;
}

int main(int argc, char **argv) {
    if (argc == 2 && strcmp(argv[1], "probe") == 0) {
        std::cout << "cpak-ui 1 " CPAK_UI_BACKEND << std::endl;
        return 0;
    }
    QApplication application(argc, argv);
    QStringList arguments = application.arguments();
    if (arguments.size() < 2 || option(arguments, QStringLiteral("--protocol")) != QStringLiteral("1")) {
        return 2;
    }
    if (arguments[1] == QStringLiteral("prompt")) {
        return prompt(arguments);
    }
    if (arguments[1] == QStringLiteral("progress")) {
        return progress(arguments);
    }
    return 2;
}
