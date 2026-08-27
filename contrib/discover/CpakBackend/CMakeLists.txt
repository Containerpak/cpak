set(cpak-backend_SRCS
    CpakBackend.cpp
    CpakResource.cpp
    CpakTransaction.cpp
)

kcoreaddons_add_plugin(cpak-backend SOURCES ${cpak-backend_SRCS} INSTALL_NAMESPACE "discover")
target_link_libraries(cpak-backend
    Qt::Core
    Qt::Gui
    KF6::CoreAddons
    Discover::Common
)

install(FILES cpak-backend-categories.xml DESTINATION ${KDE_INSTALL_DATADIR}/libdiscover/categories)
