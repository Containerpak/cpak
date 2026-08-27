/*
 * SPDX-FileCopyrightText: 2026 Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: GPL-2.0-only OR GPL-3.0-only OR LicenseRef-KDE-Accepted-GPL
 */

#pragma once

#include <QPointer>
#include <QProcess>
#include <Transaction/Transaction.h>

class CpakResource;

class CpakTransaction : public Transaction
{
    Q_OBJECT

public:
    CpakTransaction(CpakResource *resource, Role role, const QString &cpakPath);

    void cancel() override;
    void proceed() override;

private:
    void start();
    void finished(int exitCode, QProcess::ExitStatus exitStatus);

    CpakResource *m_resource;
    QString m_cpakPath;
    QPointer<QProcess> m_process;
    bool m_started = false;
};
