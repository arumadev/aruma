import 'package:flutter/material.dart';

import '../../l10n/app_localizations.dart';
import '../data/models.dart';
import '../data/repositories.dart';
import 'common.dart';

/// The largest attachment the Storage rules accept (storage.rules: under 10 MB).
const maxAttachmentMb = 10;

/// The team's tasks, live: add, tick off, assign, attach a file, delete.
class TasksTab extends StatefulWidget {
  const TasksTab({super.key, required this.teamId, required this.members});

  final String teamId;
  final List<Member> members;

  @override
  State<TasksTab> createState() => _TasksTabState();
}

class _TasksTabState extends State<TasksTab> with AutomaticKeepAliveClientMixin {
  final _title = TextEditingController();
  final _uploads = ValueNotifier<Map<String, double>>({});
  Stream<List<TaskItem>>? _tasks;
  bool _adding = false;

  @override
  bool get wantKeepAlive => true;

  @override
  void didChangeDependencies() {
    super.didChangeDependencies();
    _tasks ??= Scope.backendOf(context).tasks.watchTasks(widget.teamId);
  }

  @override
  void dispose() {
    _title.dispose();
    _uploads.dispose();
    super.dispose();
  }

  TaskRepository get _repo => Scope.backendOf(context).tasks;

  Future<void> _do(Future<void> Function() action, {String? done}) async {
    try {
      await action();
      if (done != null && mounted) showMessage(context, done);
    } catch (e) {
      if (mounted) showError(context, e);
    }
  }

  Future<void> _add() async {
    final title = _title.text.trim();
    if (title.isEmpty || _adding) return;
    setState(() => _adding = true);
    await _do(() async {
      await _repo.addTask(widget.teamId, title: title, uid: UserScope.of(context).uid);
      _title.clear();
    });
    if (mounted) setState(() => _adding = false);
  }

  Future<void> _attach(TaskItem task) async {
    final l = AppLocalizations.of(context);
    final file = await Scope.deviceOf(context).pickFile();
    if (file == null || !mounted) return;
    if (file.bytes.length >= maxAttachmentMb * 1024 * 1024) {
      showError(context, const AppException(AppErrorKind.fileTooLarge, '$maxAttachmentMb'));
      return;
    }
    void progress(double p) => _uploads.value = {..._uploads.value, task.id: p};
    progress(0);
    await _do(
      () => _repo.attachFile(widget.teamId, task,
          name: file.name, bytes: file.bytes, contentType: file.contentType, onProgress: progress),
      done: l.fileAttached,
    );
    _uploads.value = {..._uploads.value}..remove(task.id);
  }

  Future<void> _open(Attachment a) async {
    final l = AppLocalizations.of(context);
    final device = Scope.deviceOf(context);
    await _do(() async {
      final url = await _repo.attachmentUrl(a);
      if (!await device.open(url) && mounted) showMessage(context, l.couldNotOpen);
    });
  }

  Future<void> _assign(TaskItem task) async {
    final l = AppLocalizations.of(context);
    // The value is the member's uid, '' for nobody; null when the dialog was dismissed.
    final uid = await showDialog<String>(
      context: context,
      builder: (context) => SimpleDialog(
        title: Text(l.assign),
        children: [
          SimpleDialogOption(onPressed: () => Navigator.pop(context, ''), child: Text(l.unassigned)),
          for (final m in widget.members)
            SimpleDialogOption(onPressed: () => Navigator.pop(context, m.uid), child: Text(m.name)),
        ],
      ),
    );
    if (uid == null) return;
    await _do(() => _repo.setAssignee(widget.teamId, task.id, uid.isEmpty ? null : uid));
  }

  @override
  Widget build(BuildContext context) {
    super.build(context);
    final l = AppLocalizations.of(context);
    return Column(
      children: [
        Padding(
          padding: const EdgeInsets.fromLTRB(16, 16, 16, 8),
          child: Row(
            children: [
              Expanded(
                child: TextField(
                  controller: _title,
                  maxLength: 200,
                  decoration: InputDecoration(labelText: l.newTaskHint, counterText: ''),
                  textInputAction: TextInputAction.done,
                  onSubmitted: (_) => _add(),
                ),
              ),
              const SizedBox(width: 8),
              IconButton.filled(
                onPressed: _adding ? null : _add,
                tooltip: l.addTask,
                icon: const Icon(Icons.add),
              ),
            ],
          ),
        ),
        Expanded(
          child: StreamBuilder<List<TaskItem>>(
            stream: _tasks,
            builder: (context, snap) {
              if (snap.hasError) {
                return ErrorView(
                  error: snap.error!,
                  onRetry: () => setState(() => _tasks = _repo.watchTasks(widget.teamId)),
                );
              }
              final tasks = snap.data;
              if (tasks == null) return const LoadingView();
              if (tasks.isEmpty) {
                return EmptyView(icon: Icons.task_alt, title: l.noTasks, hint: l.noTasksHint);
              }
              // Open tasks first; within each group the query's order, newest first.
              final open = [for (final t in tasks) if (!t.done) t];
              final done = [for (final t in tasks) if (t.done) t];
              return ValueListenableBuilder<Map<String, double>>(
                valueListenable: _uploads,
                builder: (context, uploads, _) => ListView(
                  padding: const EdgeInsets.only(bottom: 24),
                  children: [
                    if (open.isNotEmpty) _Header(text: l.openTasks, count: open.length),
                    for (final t in open) _tile(t, uploads[t.id]),
                    if (done.isNotEmpty) _Header(text: l.doneTasks, count: done.length),
                    for (final t in done) _tile(t, uploads[t.id]),
                  ],
                ),
              );
            },
          ),
        ),
      ],
    );
  }

  Widget _tile(TaskItem task, double? upload) {
    final l = AppLocalizations.of(context);
    final theme = Theme.of(context);
    String? nameOf(String? uid) {
      if (uid == null) return null;
      for (final m in widget.members) {
        if (m.uid == uid) return m.name;
      }
      return null;
    }

    final assignee = nameOf(task.assignee);
    final attachment = task.attachment;
    return Column(
      key: ValueKey(task.id),
      mainAxisSize: MainAxisSize.min,
      children: [
        ListTile(
          leading: Checkbox(
            value: task.done,
            semanticLabel: l.taskDoneLabel(task.title),
            onChanged: (v) => _do(() => _repo.setDone(widget.teamId, task.id, v ?? false)),
          ),
          title: Text(
            task.title,
            style: task.done
                ? TextStyle(decoration: TextDecoration.lineThrough, color: theme.colorScheme.onSurfaceVariant)
                : null,
          ),
          subtitle: assignee == null && attachment == null
              ? null
              : Padding(
                  padding: const EdgeInsets.only(top: 4),
                  child: Wrap(
                    spacing: 8,
                    runSpacing: 4,
                    crossAxisAlignment: WrapCrossAlignment.center,
                    children: [
                      if (assignee != null)
                        Chip(
                          avatar: const Icon(Icons.person_outline, size: 16),
                          label: Text(l.assignedTo(assignee)),
                          visualDensity: VisualDensity.compact,
                        ),
                      if (attachment != null)
                        ActionChip(
                          avatar: const Icon(Icons.attach_file, size: 16),
                          label: Text(attachment.name, overflow: TextOverflow.ellipsis),
                          tooltip: l.openAttachment(attachment.name),
                          visualDensity: VisualDensity.compact,
                          onPressed: () => _open(attachment),
                        ),
                    ],
                  ),
                ),
          trailing: PopupMenuButton<String>(
            tooltip: l.taskMenu(task.title),
            onSelected: (v) => switch (v) {
              'attach' => _attach(task),
              'assign' => _assign(task),
              _ => _do(() => _repo.deleteTask(widget.teamId, task), done: l.taskDeleted),
            },
            itemBuilder: (context) => [
              PopupMenuItem(
                value: 'attach',
                enabled: upload == null,
                child: ListTile(
                  leading: const Icon(Icons.attach_file),
                  title: Text(attachment == null ? l.attachFile : l.replaceFile),
                ),
              ),
              PopupMenuItem(
                value: 'assign',
                child: ListTile(leading: const Icon(Icons.person_add_alt), title: Text(l.assign)),
              ),
              PopupMenuItem(
                value: 'delete',
                child: ListTile(leading: const Icon(Icons.delete_outline), title: Text(l.deleteTask)),
              ),
            ],
          ),
        ),
        if (upload != null)
          Padding(
            padding: const EdgeInsetsDirectional.fromSTEB(72, 0, 16, 8),
            child: Row(
              children: [
                Expanded(child: LinearProgressIndicator(value: upload)),
                const SizedBox(width: 12),
                Text(l.uploading((upload * 100).round()), style: theme.textTheme.labelSmall),
              ],
            ),
          ),
      ],
    );
  }
}

class _Header extends StatelessWidget {
  const _Header({required this.text, required this.count});

  final String text;
  final int count;

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    return Padding(
      padding: const EdgeInsetsDirectional.fromSTEB(16, 16, 16, 4),
      child: Text('$text ($count)', style: theme.textTheme.titleSmall?.copyWith(color: theme.colorScheme.primary)),
    );
  }
}
