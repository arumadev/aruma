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
# sets with setpriv. A process run with sudo would be root's and not guarded (apt-get from
# playwright install --with-deps), so `run` sets no_new_privs: sudo cannot gain root under the
# guard and fails. The job installs what it needs first, then runs the suite under the guard,
# where npm works from its cache only (npm_config_offline), npx installs nothing
# (npm_config_yes=false), and ARUMA_EGRESS_GUARD=1 tells the suites' scripts to install nothing
# either. Not for a laptop: it changes the machine's firewall.
#
# What it does not count:
# - DNS. The runner's resolver is systemd-resolved's stub on 127.0.0.53 (loopback), which
#   forwards a query off the machine as its own user. `check` reports how many packets the suite
#   sent to port 53 on loopback, but a lookup is not a failure here: the suites' own guards
#   refuse Google's names before any lookup (netguard.cjs, the browsers' resolver rules).
# - IPv6 without a route. An IPv6 connection on a runner without an IPv6 route fails at routing,
#   before OUTPUT, so the rules never see it (and nothing leaves). `on` probes IPv6 only when the
#   runner has a route.
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

# dns prints how many packets the guarded group sent to port 53 on loopback (the stub resolver).
dns() {
  local n=0 c ipt
  for ipt in iptables ip6tables; do
    c=$(sudo "$ipt" -nvx -L "$chain" | awk '$3 == "RETURN" && /dpt:53/ { s += $1 } END { print s + 0 }')
    n=$((n + c))
  done
  echo "$n"
}

# probe <curl args… url>: an address off the machine (not Google's) must be refused, and counted.
probe() {
  local before
  before=$(rejected)
  if "$0" run curl -sS --max-time 10 -o /dev/null "$@" 2>/dev/null; then
    echo "egress guard: the probe reached ${*: -1}, the rules do not bite" >&2
    exit 1
  fi
  if [[ $(rejected) -le $before ]]; then
    echo "egress guard: the probe to ${*: -1} was refused, but not by the guard's rules" >&2
    exit 1
  fi
}

case ${1:-} in
  on)
    sudo groupadd -f "$group"
    for ipt in iptables ip6tables; do
      sudo "$ipt" -N "$chain" 2>/dev/null || sudo "$ipt" -F "$chain"
      sudo "$ipt" -A "$chain" -o lo -p udp --dport 53 -j RETURN
      sudo "$ipt" -A "$chain" -o lo -p tcp --dport 53 -j RETURN
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
    # The probes: an address off the machine (not Google's) must be refused, and counted; sudo
    # must fail under the guard.
    probe http://1.1.1.1/
    if ip -6 route get 2606:4700:4700::1111 >/dev/null 2>&1; then
      probe -6 'http://[2606:4700:4700::1111]/'
      v6="IPv4 and IPv6 probed"
    else
      v6="IPv4 probed; no IPv6 route on this runner"
    fi
    if "$0" run sudo -n true 2>/dev/null; then
      echo "egress guard: sudo gained root under the guard" >&2
      exit 1
    fi
    for ipt in iptables ip6tables; do sudo "$ipt" -Z "$chain"; done
    sudo dmesg -C 2>/dev/null || true
    touch "$marker"
    echo "egress guard on: group $group may reach loopback and the service containers only ($v6; sudo refused)"
    ;;
  run)
    shift
    [[ $# -gt 0 ]] || { echo "usage: $0 run CMD [ARG…]" >&2; exit 2; }
    exec sudo --preserve-env setpriv --reuid="$(id -u)" --regid="$group" --init-groups --no-new-privs -- \
      env PATH="$PATH" HOME="$HOME" ARUMA_EGRESS_GUARD=1 npm_config_offline=true npm_config_yes=false \
      npm_config_update_notifier=false "$@"
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
    echo "egress guard: no packet went to an address off the machine ($(dns) to the local DNS resolver, not counted: see the header)"
    ;;
  *)
    echo "usage: $0 on | run CMD [ARG…] | check" >&2
    exit 2
    ;;
esac
