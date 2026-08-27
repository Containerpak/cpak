/*
 * SPDX-FileCopyrightText: 2026 Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: GPL-2.0-only OR GPL-3.0-only OR LicenseRef-KDE-Accepted-GPL
 */

#include "CpakBackend.h"
#include "CpakResource.h"
#include "CpakTransaction.h"

#include <QJsonArray>
#include <QJsonDocument>
#include <QJsonObject>
#include <QStandardPaths>
#include <QTimer>
#include <resources/StandardBackendUpdater.h>

DISCOVER_BACKEND_PLUGIN(CpakBackend)

static constexpr qsizetype maximumCatalogSize = 16 * 1024 * 1024;

CpakBackend::CpakBackend(QObject *parent)
    : AbstractResourcesBackend(parent)
    , m_cpakPath(QStandardPaths::findExecutable(QStringLiteral("cpak")))
    , m_updater(new StandardBackendUpdater(this))
{
    connect(m_updater, &StandardBackendUpdater::updatesCountChanged, this, &CpakBackend::updatesCountChanged);
    if (!m_cpakPath.isEmpty()) {
        QTimer::singleShot(0, this, &CpakBackend::checkForUpdates);
    }
}

int CpakBackend::updatesCount() const
{
    return m_updater->updatesCount();
}

AbstractBackendUpdater *CpakBackend::backendUpdater() const
{
    return m_updater;
}

AbstractReviewsBackend *CpakBackend::reviewsBackend() const
{
    return nullptr;
}

ResultsStream *CpakBackend::search(const Filters &filter)
{
    QVector<StreamResult> results;
    if (!filter.resourceUrl.isEmpty()) {
        if (filter.resourceUrl.scheme() == QLatin1String("cpak")) {
            const QString origin = filter.resourceUrl.host() + filter.resourceUrl.path();
            if (auto *resource = m_resources.value(origin)) {
                results.append(resource);
            }
        }
        return new ResultsStream(QStringLiteral("cpak-url"), results);
    }
    for (auto *resource : std::as_const(m_resources)) {
        if (!filter.search.isEmpty() && !resource->name().contains(filter.search, Qt::CaseInsensitive)
            && !resource->comment().contains(filter.search, Qt::CaseInsensitive) && !resource->packageName().contains(filter.search, Qt::CaseInsensitive)) {
            continue;
        }
        if (!filter.shouldFilter(resource)) {
            results.append(resource);
        }
    }
    return new ResultsStream(QStringLiteral("cpak-search"), results);
}

bool CpakBackend::isValid() const
{
    return !m_cpakPath.isEmpty();
}

Transaction *CpakBackend::installApplication(AbstractResource *app)
{
    auto *resource = qobject_cast<CpakResource *>(app);
    return resource ? new CpakTransaction(resource, Transaction::InstallRole, m_cpakPath) : nullptr;
}

Transaction *CpakBackend::installApplication(AbstractResource *app, const AddonList &addons)
{
    Q_UNUSED(addons)
    return installApplication(app);
}

Transaction *CpakBackend::removeApplication(AbstractResource *app)
{
    auto *resource = qobject_cast<CpakResource *>(app);
    return resource ? new CpakTransaction(resource, Transaction::RemoveRole, m_cpakPath) : nullptr;
}

void CpakBackend::checkForUpdates()
{
    if (m_fetching || m_cpakPath.isEmpty()) {
        return;
    }
    m_fetching = true;
    Q_EMIT fetchingUpdatesProgressChanged();
    auto *process = new QProcess(this);
    m_catalogProcess = process;
    process->setProgram(m_cpakPath);
    process->setArguments({QStringLiteral("discover"), QStringLiteral("list")});
    connect(process, &QProcess::finished, this, &CpakBackend::catalogFinished);
    process->start();
}

QString CpakBackend::displayName() const
{
    return QStringLiteral("cpak");
}

bool CpakBackend::hasApplications() const
{
    return true;
}

int CpakBackend::fetchingUpdatesProgress() const
{
    return m_fetching ? 0 : 100;
}

const QString &CpakBackend::cpakPath() const
{
    return m_cpakPath;
}

void CpakBackend::catalogFinished(int exitCode, QProcess::ExitStatus exitStatus)
{
    auto *process = m_catalogProcess.data();
    m_catalogProcess.clear();
    m_fetching = false;
    Q_EMIT fetchingUpdatesProgressChanged();
    if (!process) {
        return;
    }
    const QByteArray output = process->readAllStandardOutput();
    const QString error = QString::fromUtf8(process->readAllStandardError().left(4096)).trimmed();
    process->deleteLater();
    if (exitStatus != QProcess::NormalExit || exitCode != 0 || output.size() > maximumCatalogSize) {
        Q_EMIT passiveMessage(error.isEmpty() ? QStringLiteral("cpak could not load its catalog") : error);
        return;
    }
    QJsonParseError parseError;
    const QJsonDocument document = QJsonDocument::fromJson(output, &parseError);
    const QJsonObject root = document.object();
    if (parseError.error != QJsonParseError::NoError || root.value(QStringLiteral("schema")).toInt() != 1
        || !root.value(QStringLiteral("packages")).isArray()) {
        Q_EMIT passiveMessage(QStringLiteral("cpak returned an invalid catalog"));
        return;
    }
    replaceResources(root.value(QStringLiteral("packages")).toArray());
}

void CpakBackend::replaceResources(const QJsonArray &packages)
{
    QHash<QString, CpakResource *> next;
    for (const QJsonValue &value : packages) {
        const QJsonObject item = value.toObject();
        const QString origin = item.value(QStringLiteral("origin")).toString();
        if (origin.isEmpty() || next.contains(origin)) {
            continue;
        }
        next.insert(origin, new CpakResource(item, this));
    }
    for (auto *resource : std::as_const(m_resources)) {
        Q_EMIT resourceRemoved(resource);
        resource->deleteLater();
    }
    m_resources = next;
    Q_EMIT contentsChanged();
    Q_EMIT updatesCountChanged();
}

#include "CpakBackend.moc"
#include "moc_CpakBackend.cpp"
