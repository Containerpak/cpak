/*
 * SPDX-FileCopyrightText: 2026 Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: GPL-2.0-only OR GPL-3.0-only OR LicenseRef-KDE-Accepted-GPL
 */

#include "CpakResource.h"

#include <QIcon>
#include <QPixmap>

CpakResource::CpakResource(const QJsonObject &data, AbstractResourcesBackend *parent)
    : AbstractResource(parent)
    , m_origin(data.value(QStringLiteral("origin")).toString())
    , m_name(data.value(QStringLiteral("name")).toString())
    , m_description(data.value(QStringLiteral("description")).toString())
    , m_availableVersion(data.value(QStringLiteral("available_version")).toString())
    , m_installedVersion(data.value(QStringLiteral("installed_version")).toString())
    , m_iconSvg(data.value(QStringLiteral("icon_svg")).toString())
    , m_iconPng(data.value(QStringLiteral("icon_png")).toString())
    , m_permissions(data.value(QStringLiteral("permissions")).toArray())
    , m_state(data.value(QStringLiteral("installed")).toBool() ? Installed : None)
    , m_installable(data.value(QStringLiteral("installable")).toBool())
{
}

QString CpakResource::packageName() const
{
    return m_origin;
}
QString CpakResource::name() const
{
    return m_name;
}
QString CpakResource::comment()
{
    return m_description;
}
bool CpakResource::canExecute() const
{
    return false;
}
void CpakResource::invokeApplication() const
{
}
CpakResource::State CpakResource::state()
{
    return m_state;
}
bool CpakResource::hasCategory(const QString &category) const
{
    return category == QLatin1String("cpak");
}
CpakResource::Type CpakResource::type() const
{
    return Application;
}
quint64 CpakResource::size()
{
    return 0;
}
QJsonArray CpakResource::licenses()
{
    return {};
}
QString CpakResource::installedVersion() const
{
    return m_installedVersion;
}
QString CpakResource::availableVersion() const
{
    return m_availableVersion;
}
QString CpakResource::origin() const
{
    return QStringLiteral("cpak");
}
QString CpakResource::section()
{
    return QStringLiteral("Applications");
}
QString CpakResource::author() const
{
    return m_origin.section(QLatin1Char('/'), 1, 1);
}
QList<PackageState> CpakResource::addonsInformation()
{
    return {};
}
QString CpakResource::sourceIcon() const
{
    return QStringLiteral("system-software-install");
}
QDate CpakResource::releaseDate() const
{
    return {};
}
QUrl CpakResource::homepage()
{
    return QUrl(QStringLiteral("https://") + m_origin);
}
QUrl CpakResource::bugURL()
{
    return QUrl(QStringLiteral("https://") + m_origin + QStringLiteral("/issues"));
}
QUrl CpakResource::url() const
{
    return QUrl(QStringLiteral("cpak://") + m_origin);
}
void CpakResource::fetchChangelog()
{
    Q_EMIT changelogFetched({});
}

QVariant CpakResource::icon() const
{
    QPixmap pixmap;
    if (!m_iconSvg.isEmpty() && pixmap.loadFromData(m_iconSvg.toUtf8(), "SVG")) {
        return QIcon(pixmap);
    }
    if (!m_iconPng.isEmpty() && pixmap.loadFromData(QByteArray::fromBase64(m_iconPng.toUtf8()), "PNG")) {
        return QIcon(pixmap);
    }
    return QStringLiteral("system-software-install");
}

QString CpakResource::longDescription()
{
    QString result = m_description.toHtmlEscaped();
    if (!m_installable && m_state != Installed) {
        result += QStringLiteral("<p>This package can be viewed now and installed after the next signed cpak catalog is published.</p>");
    }
    result += permissionDescription();
    return result;
}

bool CpakResource::isInstallable() const
{
    return m_installable;
}

QString CpakResource::permissionDescription() const
{
    QString result = QStringLiteral("<h3>Permissions</h3><ul>");
    if (m_permissions.isEmpty()) {
        return result + QStringLiteral("<li>None</li></ul>");
    }
    for (const QJsonValue &value : m_permissions) {
        const QJsonObject permission = value.toObject();
        result += QStringLiteral("<li><b>%1</b>: %2</li>")
                      .arg(permission.value(QStringLiteral("name")).toString().toHtmlEscaped(),
                           permission.value(QStringLiteral("detail")).toString().toHtmlEscaped());
    }
    return result + QStringLiteral("</ul>");
}

void CpakResource::setState(State state)
{
    if (m_state == state) {
        return;
    }
    m_state = state;
    if (state == Installed) {
        m_installedVersion = m_availableVersion;
    } else if (state == None) {
        m_installedVersion.clear();
    }
    Q_EMIT stateChanged();
    Q_EMIT versionsChanged();
}

#include "moc_CpakResource.cpp"
