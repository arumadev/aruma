#!/usr/bin/env bash
# The CI half of the network guard, on GitHub's Ubuntu runners (iptables, passwordless sudo): a
# command run under it may reach loopback and the job's service containers, and nothing else.
# Every other packet it sends is rejected, logged to the kernel log and counted, whatever the
# program (Go, Node, a browser, a build tool), and `check` fails the job when the count is not
# zero. The suites keep their own guards (netguard.cjs, the browsers' resolver rules, the Go
# programs' dialers); this one also catches what slips past them.
#
#   test/ci/egress-guard.sh on            the rules, then a probe that proves they bite
#   test/ci/egress-guard.sh run CMD ARG…  CMD with the guard's group, as the runner's user
#   test/ci/egress-guard.sh check         fails unless `on` ran and nothing was rejected since
#
# The rules match the group of the sending process (iptables -m owner --gid-owner), which `run`
# sets with setpriv; a process run with sudo inside is root's and is not guarded (apt-get from
# playwright install --with-deps). So the job installs what it needs first, then runs the suite
# under the guard, where npm works from its cache only (npm_config_offline). Not for a laptop:
# it changes the machine's firewall.
set -euo pipefail

group=aruma-offline
chain=ARUMA_EGRESS
marker=${RUNNER_TEMP:-/tmp}/aruma-egress-guard.on

# rejected prints how many packets the chain has rejected, IPv4 and IPv6 together.
rejected() {
  local n=0 c ipt
  for ipt in iptables ip6tables; do
    c=$(sudo "$ipt" -nvx -L "$chain" | awk '$3 == "REJECT" { print $1 }')
    n=$((n + ${c:-0}))
  done
  echo "$n"
}

# log prints the kernel's log lines of rejected packets.
log() { sudo dmesg 2>/dev/null | grep 'aruma-egress: ' || true; }

case ${1:-} in
  on)
    sudo groupadd -f "$group"
    for ipt in iptables ip6tables; do
      sudo "$ipt" -N "$chain" 2>/dev/null || sudo "$ipt" -F "$chain"
      sudo "$ipt" -A "$chain" -o lo -j RETURN
      # The job's service containers (Postgres): Docker's default bridge and the network the
      # runner makes for the job.
      sudo "$ipt" -A "$chain" -o docker0 -j RETURN
      sudo "$ipt" -A "$chain" -o br-+ -j RETURN
      sudo "$ipt" -A "$chain" -j LOG --log-prefix 'aruma-egress: ' --log-uid
      sudo "$ipt" -A "$chain" -j REJECT
      sudo "$ipt" -C OUTPUT -m owner --gid-owner "$group" -j "$chain" 2>/dev/null ||
        sudo "$ipt" -I OUTPUT 1 -m owner --gid-owner "$group" -j "$chain"
    done
    # The probe: an address off the machine (not Google's) must be refused, and counted.
    if "$0" run curl -sS --max-time 10 -o /dev/null http://1.1.1.1/ 2>/dev/null; then
      echo "egress guard: the probe reached 1.1.1.1, the rules do not bite" >&2
      exit 1
    fi
    if [[ $(rejected) -eq 0 ]]; then
      echo "egress guard: the probe was refused, but not by the guard's rules" >&2
      exit 1
    fi
    for ipt in iptables ip6tables; do sudo "$ipt" -Z "$chain"; done
    sudo dmesg -C 2>/dev/null || true
    touch "$marker"
    echo "egress guard on: group $group may reach loopback and the service containers only"
    ;;
  run)
    shift
    [[ $# -gt 0 ]] || { echo "usage: $0 run CMD [ARG…]" >&2; exit 2; }
    exec sudo --preserve-env setpriv --reuid="$(id -u)" --regid="$group" --init-groups -- \
      env PATH="$PATH" HOME="$HOME" npm_config_offline=true npm_config_update_notifier=false "$@"
    ;;
  check)
    if [[ ! -f $marker ]]; then
      echo "egress guard: it was never turned on in this job" >&2
      exit 1
    fi
    n=$(rejected)
    if [[ $n -ne 0 ]]; then
      echo "egress guard: $n packets tried to leave the machine:" >&2
      log >&2
      exit 1
    fi
    echo "egress guard: nothing tried to leave the machine"
    ;;
  *)
    echo "usage: $0 on | run CMD [ARG…] | check" >&2
    exit 2
    ;;
esac
