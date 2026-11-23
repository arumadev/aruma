import 'package:flutter/material.dart';

import '../../l10n/app_localizations.dart';
import '../data/models.dart';
import 'common.dart';
import 'profile_page.dart';
import 'team_page.dart';

/// The teams the user belongs to; create one, or join one by its code.
class TeamsPage extends StatefulWidget {
  const TeamsPage({super.key});

  @override
  State<TeamsPage> createState() => _TeamsPageState();
}

class _TeamsPageState extends State<TeamsPage> {
  Stream<List<TeamSummary>>? _teams;
  String? _uid;

  @override
  void didChangeDependencies() {
    super.didChangeDependencies();
    final uid = UserScope.of(context).uid;
    if (uid != _uid) {
      _uid = uid;
      _teams = Scope.backendOf(context).teams.watchMyTeams(uid);
    }
  }

  void _retry() => setState(() => _teams = Scope.backendOf(context).teams.watchMyTeams(_uid!));

  void _open(TeamSummary team) {
    Navigator.of(context).push(MaterialPageRoute<void>(builder: (_) => TeamPage(teamId: team.id, initialName: team.name)));
  }

  Future<void> _create() async {
    final l = AppLocalizations.of(context);
    final user = UserScope.of(context);
    final backend = Scope.backendOf(context);
    final team = await showDialog<TeamSummary>(
      context: context,
      builder: (_) => _TextDialog(
        title: l.createTeam,
        label: l.teamNameLabel,
        action: l.create,
        maxLength: 60,
        run: (name) async => TeamSummary(
          id: await backend.teams.createTeam(name: name, uid: user.uid, userName: user.name),
          name: name,
          role: 'owner',
        ),
      ),
    );
    if (team == null || !mounted) return;
    showMessage(context, l.teamCreated(team.id));
    _open(team);
  }

  Future<void> _join() async {
    final l = AppLocalizations.of(context);
    final user = UserScope.of(context);
    final backend = Scope.backendOf(context);
    final team = await showDialog<TeamSummary>(
      context: context,
      builder: (_) => _TextDialog(
        title: l.joinTeam,
        label: l.joinCodeLabel,
        action: l.join,
        maxLength: 64,
        run: (code) => backend.teams.joinTeam(code: code, uid: user.uid, userName: user.name),
      ),
    );
    if (team == null || !mounted) return;
    showMessage(context, l.joinedTeam(team.name));
    _open(team);
  }

  @override
  Widget build(BuildContext context) {
    final l = AppLocalizations.of(context);
    final user = UserScope.of(context);
    return Scaffold(
      appBar: AppBar(
        title: Text(l.myTeams),
        actions: [
          IconButton(icon: const Icon(Icons.group_add_outlined), tooltip: l.joinTeam, onPressed: _join),
          IconButton(
            tooltip: l.profile,
            icon: ExcludeSemantics(child: Avatar(name: user.name, photoUrl: user.profile?.photoUrl, radius: 16)),
            onPressed: () => Navigator.of(context).push(MaterialPageRoute<void>(builder: (_) => const ProfilePage())),
          ),
        ],
      ),
      floatingActionButton: FloatingActionButton.extended(
        onPressed: _create,
        icon: const Icon(Icons.add),
        label: Text(l.createTeam),
      ),
      body: Column(
        children: [
          const OfflineBanner(),
          Expanded(
            child: StreamBuilder<List<TeamSummary>>(
              stream: _teams,
              builder: (context, snap) {
                if (snap.hasError) return ErrorView(error: snap.error!, onRetry: _retry);
                final teams = snap.data;
                if (teams == null) return const LoadingView();
                if (teams.isEmpty) {
                  return EmptyView(
                    icon: Icons.groups_outlined,
                    title: l.noTeams,
                    hint: l.noTeamsHint,
                    actions: [OutlinedButton.icon(onPressed: _join, icon: const Icon(Icons.group_add_outlined), label: Text(l.joinTeam))],
                  );
                }
                return ListView.separated(
                  padding: const EdgeInsets.only(bottom: 88),
                  itemCount: teams.length,
                  separatorBuilder: (_, _) => const Divider(height: 1),
                  itemBuilder: (context, i) {
                    final t = teams[i];
                    return ListTile(
                      leading: CircleAvatar(child: Text(t.name.isEmpty ? '?' : t.name.characters.first.toUpperCase())),
                      title: Text(t.name),
                      subtitle: Text('${l.joinCodeLabel}: ${t.id}'),
                      trailing: t.isOwner ? Chip(label: Text(l.ownerBadge)) : const Icon(Icons.chevron_right),
                      onTap: () => _open(t),
                    );
                  },
                );
              },
            ),
          ),
        ],
      ),
    );
  }
}

/// A dialog with one field that runs [run] and closes with its result, or shows its error.
class _TextDialog<T> extends StatefulWidget {
  const _TextDialog({required this.title, required this.label, required this.action, required this.maxLength, required this.run});

  final String title;
  final String label;
  final String action;
  final int maxLength;
  final Future<T> Function(String value) run;

  @override
  State<_TextDialog<T>> createState() => _TextDialogState<T>();
}

class _TextDialogState<T> extends State<_TextDialog<T>> {
  final _controller = TextEditingController();
  bool _busy = false;
  Object? _error;

  @override
  void dispose() {
    _controller.dispose();
    super.dispose();
  }

  Future<void> _submit() async {
    final value = _controller.text.trim();
    if (value.isEmpty || _busy) return;
    setState(() {
      _busy = true;
      _error = null;
    });
    try {
      final result = await widget.run(value);
      if (mounted) Navigator.of(context).pop(result);
    } catch (e) {
      if (mounted) setState(() => _error = e);
    } finally {
      if (mounted) setState(() => _busy = false);
    }
  }

  @override
  Widget build(BuildContext context) {
    final l = AppLocalizations.of(context);
    return AlertDialog(
      title: Text(widget.title),
      content: TextField(
        controller: _controller,
        autofocus: true,
        maxLength: widget.maxLength,
        decoration: InputDecoration(
          labelText: widget.label,
          errorText: _error == null ? null : errorText(context, _error!),
          errorMaxLines: 3,
        ),
        onSubmitted: (_) => _submit(),
      ),
      actions: [
        TextButton(onPressed: _busy ? null : () => Navigator.of(context).pop(), child: Text(l.cancel)),
        FilledButton(
          onPressed: _busy ? null : _submit,
          child: _busy
              ? const SizedBox.square(dimension: 18, child: CircularProgressIndicator(strokeWidth: 2))
              : Text(widget.action),
        ),
      ],
    );
  }
}
