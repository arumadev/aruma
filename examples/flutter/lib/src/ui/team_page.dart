import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

import '../../l10n/app_localizations.dart';
import '../data/models.dart';
import '../data/repositories.dart';
import 'chat_tab.dart';
import 'common.dart';
import 'tasks_tab.dart';

/// One team: its tasks and its chat. While it is open, the user is shown online to the team.
class TeamPage extends StatefulWidget {
  const TeamPage({super.key, required this.teamId, required this.initialName});

  final String teamId;
  final String initialName;

  @override
  State<TeamPage> createState() => _TeamPageState();
}

class _TeamPageState extends State<TeamPage> {
  PresenceHandle? _presence;
  late Stream<Team?> _team;
  late Stream<List<Member>> _members;

  @override
  void didChangeDependencies() {
    super.didChangeDependencies();
    if (_presence != null) return;
    final backend = Scope.backendOf(context);
    final user = UserScope.of(context);
    _presence = backend.presence.enter(widget.teamId, uid: user.uid, name: user.name);
    _team = backend.teams.watchTeam(widget.teamId);
    _members = backend.teams.watchMembers(widget.teamId);
  }

  @override
  void dispose() {
    _presence?.close();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final l = AppLocalizations.of(context);
    return DefaultTabController(
      length: 2,
      child: Scaffold(
        appBar: AppBar(
          title: StreamBuilder<Team?>(
            stream: _team,
            builder: (context, snap) => Text(snap.data?.name ?? widget.initialName),
          ),
          actions: [
            Padding(
              padding: const EdgeInsetsDirectional.only(end: 8),
              child: Tooltip(
                message: l.joinCodeTooltip(widget.teamId),
                child: ActionChip(
                  avatar: const Icon(Icons.key_outlined, size: 18),
                  label: Text(widget.teamId),
                  onPressed: () async {
                    await Clipboard.setData(ClipboardData(text: widget.teamId));
                    if (context.mounted) showMessage(context, l.codeCopied);
                  },
                ),
              ),
            ),
          ],
          bottom: TabBar(tabs: [
            Tab(icon: const Icon(Icons.checklist), text: l.tasksTab),
            Tab(icon: const Icon(Icons.forum_outlined), text: l.chatTab),
          ]),
        ),
        body: Column(
          children: [
            const OfflineBanner(),
            Expanded(
              child: StreamBuilder<List<Member>>(
                stream: _members,
                builder: (context, snap) {
                  final members = snap.data ?? const <Member>[];
                  return TabBarView(children: [
                    TasksTab(teamId: widget.teamId, members: members),
                    ChatTab(teamId: widget.teamId),
                  ]);
                },
              ),
            ),
          ],
        ),
      ),
    );
  }
}
