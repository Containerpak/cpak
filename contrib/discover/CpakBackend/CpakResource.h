/*
 * SPDX-FileCopyrightText: 2026 Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: GPL-2.0-only OR GPL-3.0-only OR LicenseRef-KDE-Accepted-GPL
 */

#pragma once

#include <QJsonArray>
#include <QJsonObject>
#include <resources/AbstractResource.h>

class CpakResource : public AbstractResource
{
    Q_OBJECT

public:
    CpakResource(const QJsonObject &data, AbstractResourcesBackend *parent);

    QString packageName() const override;
    QString name() const override;
    QString comment() override;
    QVariant icon() const override;
    bool canExecute() const override;
    void invokeApplication() const override;
    State state() override;
    bool hasCategory(const QString &category) const override;
    Type type() const override;
    quint64 size() override;
    QJsonArray licenses() override;
    QString installedVersion() const override;
    QString availableVersion() const override;
    QString longDescription() override;
    QString origin() const override;
    QString section() override;
    QString author() const override;
    QList<PackageState> addonsInformation() override;
    QString sourceIcon() const override;
    QDate releaseDate() const override;
    QUrl homepage() override;
    QUrl bugURL() override;
    QUrl url() const override;
    void fetchChangelog() override;

    bool isInstallable() const;
    QString permissionDescription() const;
    void setState(State state);

private:
    QString m_origin;
    QString m_name;
    QString m_description;
    QString m_availableVersion;
    QString m_installedVersion;
    QString m_iconSvg;
    QString m_iconPng;
    QJsonArray m_permissions;
    State m_state;
    bool m_installable;
};
