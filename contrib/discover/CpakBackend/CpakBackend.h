/*
 * SPDX-FileCopyrightText: 2026 Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: GPL-2.0-only OR GPL-3.0-only OR LicenseRef-KDE-Accepted-GPL
 */

#pragma once

#include <QHash>
#include <QPointer>
#include <QProcess>
#include <resources/AbstractResourcesBackend.h>

class CpakResource;
class StandardBackendUpdater;

class CpakBackend : public AbstractResourcesBackend
{
    Q_OBJECT

public:
    explicit CpakBackend(QObject *parent = nullptr);

    int updatesCount() const override;
    AbstractBackendUpdater *backendUpdater() const override;
    AbstractReviewsBackend *reviewsBackend() const override;
    ResultsStream *search(const Filters &filter) override;
    bool isValid() const override;
    Transaction *installApplication(AbstractResource *app) override;
    Transaction *installApplication(AbstractResource *app, const AddonList &addons) override;
    Transaction *removeApplication(AbstractResource *app) override;
    void checkForUpdates() override;
    QString displayName() const override;
    bool hasApplications() const override;
    int fetchingUpdatesProgress() const override;

    const QString &cpakPath() const;

private:
    void catalogFinished(int exitCode, QProcess::ExitStatus exitStatus);
    void replaceResources(const QJsonArray &packages);

    QString m_cpakPath;
    QPointer<QProcess> m_catalogProcess;
    QHash<QString, CpakResource *> m_resources;
    StandardBackendUpdater *m_updater;
    bool m_fetching = false;
};
