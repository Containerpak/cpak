/*
 * SPDX-FileCopyrightText: 2026 Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: GPL-2.0-only OR GPL-3.0-only OR LicenseRef-KDE-Accepted-GPL
 */

#include "CpakTransaction.h"
#include "CpakResource.h"

#include <QTimer>
#include <resources/AbstractResourcesBackend.h>

CpakTransaction::CpakTransaction(CpakResource *resource, Role role, const QString &cpakPath)
    : Transaction(resource->backend(), resource, role)
    , m_resource(resource)
    , m_cpakPath(cpakPath)
{
    setCancellable(true);
    if (role == InstallRole) {
        QTimer::singleShot(0, this, [this]() {
            if (!m_resource->isInstallable()) {
                Q_EMIT passiveMessage(QStringLiteral("The current catalog is browse-only. Install after the next cpak release."));
                setStatus(DoneWithErrorStatus);
                deleteLater();
                return;
            }
            Q_EMIT proceedRequest(QStringLiteral("Review cpak permissions"), m_resource->permissionDescription());
        });
    } else {
        QTimer::singleShot(0, this, &CpakTransaction::start);
    }
}

void CpakTransaction::proceed()
{
    start();
}

void CpakTransaction::cancel()
{
    if (m_process) {
        m_process->kill();
    }
    setStatus(CancelledStatus);
    deleteLater();
}

void CpakTransaction::start()
{
    if (m_started) {
        return;
    }
    m_started = true;
    setStatus(CommittingStatus);
    auto *process = new QProcess(this);
    m_process = process;
    process->setProgram(m_cpakPath);
    const QString action = role() == InstallRole ? QStringLiteral("install") : QStringLiteral("remove");
    process->setArguments({QStringLiteral("discover"), action, m_resource->packageName()});
    connect(process, &QProcess::finished, this, &CpakTransaction::finished);
    process->start();
}

void CpakTransaction::finished(int exitCode, QProcess::ExitStatus exitStatus)
{
    if (!m_process) {
        return;
    }
    const QString error = QString::fromUtf8(m_process->readAllStandardError().left(4096)).trimmed();
    m_process->deleteLater();
    m_process.clear();
    if (exitStatus != QProcess::NormalExit || exitCode != 0) {
        Q_EMIT passiveMessage(error.isEmpty() ? QStringLiteral("cpak transaction failed") : error);
        setStatus(DoneWithErrorStatus);
        deleteLater();
        return;
    }
    m_resource->setState(role() == InstallRole ? AbstractResource::Installed : AbstractResource::None);
    setProgress(100);
    setStatus(DoneStatus);
    deleteLater();
}

#include "moc_CpakTransaction.cpp"
