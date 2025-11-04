#!/bin/sh
# Based on https://nfpm.goreleaser.com/tips/
if ! command -V systemctl >/dev/null 2>&1; then
  echo "Could not find systemd. Skipping system installation." && exit 0
else
    systemd_version=$(systemctl --version | awk '/systemd /{print $2}')
fi

cleanInstall() {
    printf "Post Install of a clean install"

    if ! getent group  k8s-node-external-ip-watcher >/dev/null 2>&1; then
        groupadd --system k8s-node-external-ip-watcher
    fi
    # Create the user
    if ! id k8s-node-external-ip-watcher > /dev/null 2>&1 ; then
        adduser --system --home /var/lib/k8s-node-external-ip-watcher --gid "$(getent group k8s-node-external-ip-watcher | awk -F ":" '{ print $3 }')" --shell /bin/false "k8s-node-external-ip-watcher"
    fi
    
    mkdir -p /etc/k8s-node-external-ip-watcher
    mkdir -p /var/lib/k8s-node-external-ip-watcher
    chown k8s-node-external-ip-watcher:k8s-node-external-ip-watcher /var/lib/k8s-node-external-ip-watcher

    # rhel/centos7 cannot use ExecStartPre=+ to specify the pre start should be run as root
    # even if you want your service to run as non root.
    if [ "${systemd_version}" -lt 231 ]; then
        printf "systemd version %s is less then 231, fixing the service file" "${systemd_version}"
        sed -i "s/=+/=/g" /etc/systemd/system/k8s-node-external-ip-watcher.service
    fi
    printf "Reload the service unit from disk\n"
    systemctl daemon-reload ||:
    #printf "Unmask the service\n"
    #systemctl unmask k8s-node-external-ip-watcher ||:
    #printf "Set the preset flag for the service unit\n"
    #systemctl preset k8s-node-external-ip-watcher ||:
    #printf "Set the enabled flag for the service unit\n"
    #systemctl enable k8s-node-external-ip-watcher ||:
    #systemctl restart k8s-node-external-ip-watcher ||:
}

upgrade() {
    :
}

action="$1"
if  [ "$1" = "configure" ] && [ -z "$2" ]; then
  # Alpine linux does not pass args, and deb passes $1=configure
  action="install"
elif [ "$1" = "configure" ] && [ -n "$2" ]; then
    # deb passes $1=configure $2=<current version>
    action="upgrade"
fi

case "${action}" in
  "1" | "install")
    cleanInstall
    ;;
  "2" | "upgrade")
    upgrade
    ;;
  *)
    # $1 == version being installed
    printf "Alpine"
    cleanInstall
    ;;
esac
