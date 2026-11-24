import 'package:flutter/material.dart';
import 'package:intl/intl.dart' show Bidi, DateFormat;

import '../../l10n/app_localizations.dart';
import '../data/models.dart';
import 'common.dart';

/// The team's chat (the last 50 messages, live) and who is online.
class ChatTab extends StatefulWidget {
  const ChatTab({super.key, required this.teamId});

  final String teamId;

  @override
  State<ChatTab> createState() => _ChatTabState();
}

class _ChatTabState extends State<ChatTab> with AutomaticKeepAliveClientMixin {
  final _text = TextEditingController();
  Stream<List<ChatMessage>>? _messages;
  Stream<List<PresenceEntry>>? _online;
  bool _sending = false;

  @override
  bool get wantKeepAlive => true;

  @override
  void didChangeDependencies() {
    super.didChangeDependencies();
    final backend = Scope.backendOf(context);
    _messages ??= backend.chat.watchMessages(widget.teamId);
    _online ??= backend.presence.watchOnline(widget.teamId);
  }

  @override
  void dispose() {
    _text.dispose();
    super.dispose();
  }

  Future<void> _send() async {
    final text = _text.text.trim();
    if (text.isEmpty || _sending) return;
    final user = UserScope.of(context);
    setState(() => _sending = true);
    try {
      await Scope.backendOf(context).chat.send(widget.teamId, uid: user.uid, name: user.name, text: text);
      _text.clear();
    } catch (e) {
      if (mounted) showError(context, e);
    } finally {
      if (mounted) setState(() => _sending = false);
    }
  }

  @override
  Widget build(BuildContext context) {
    super.build(context);
    final l = AppLocalizations.of(context);
    final me = UserScope.of(context).uid;
    return Column(
      children: [
        StreamBuilder<List<PresenceEntry>>(
          stream: _online,
          builder: (context, snap) {
            final names = [
              for (final p in snap.data ?? const <PresenceEntry>[]) p.uid == me ? '${p.name} (${l.you})' : p.name,
            ]..sort();
            return Container(
              width: double.infinity,
              color: Theme.of(context).colorScheme.surfaceContainerHigh,
              padding: const EdgeInsets.symmetric(horizontal: 16, vertical: 8),
              child: Row(
                children: [
                  Icon(Icons.circle, size: 10, color: names.isEmpty ? Colors.grey : Colors.green),
                  const SizedBox(width: 8),
                  Expanded(
                    child: Semantics(
                      container: true,
                      liveRegion: true,
                      child: Text(l.onlineNow(names.join(l.listSeparator)), maxLines: 2, overflow: TextOverflow.ellipsis),
                    ),
                  ),
                ],
              ),
            );
          },
        ),
        Expanded(
          child: StreamBuilder<List<ChatMessage>>(
            stream: _messages,
            builder: (context, snap) {
              if (snap.hasError) {
                return ErrorView(
                  error: snap.error!,
                  onRetry: () => setState(() => _messages = Scope.backendOf(context).chat.watchMessages(widget.teamId)),
                );
              }
              final messages = snap.data;
              if (messages == null) return const LoadingView();
              if (messages.isEmpty) {
                return EmptyView(icon: Icons.forum_outlined, title: l.noMessages, hint: l.noMessagesHint);
              }
              // Newest at the bottom, and the list starts there.
              return ListView.builder(
                reverse: true,
                padding: const EdgeInsets.all(12),
                itemCount: messages.length,
                itemBuilder: (context, i) {
                  final m = messages[messages.length - 1 - i];
                  return _Bubble(message: m, mine: m.uid == me);
                },
              );
            },
          ),
        ),
        SafeArea(
          top: false,
          child: Padding(
            padding: const EdgeInsets.fromLTRB(12, 4, 12, 12),
            child: Row(
              children: [
                Expanded(
                  child: TextField(
                    controller: _text,
                    maxLength: 1000,
                    minLines: 1,
                    maxLines: 4,
                    decoration: InputDecoration(labelText: l.messageHint, counterText: ''),
                    textInputAction: TextInputAction.send,
                    onSubmitted: (_) => _send(),
                  ),
                ),
                const SizedBox(width: 8),
                IconButton.filled(onPressed: _sending ? null : _send, tooltip: l.send, icon: const Icon(Icons.send)),
              ],
            ),
          ),
        ),
      ],
    );
  }
}

class _Bubble extends StatelessWidget {
  const _Bubble({required this.message, required this.mine});

  final ChatMessage message;
  final bool mine;

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    final scheme = theme.colorScheme;
    final time = message.sentAt == null
        ? ''
        : DateFormat.Hm(Localizations.localeOf(context).toLanguageTag()).format(message.sentAt!);
    return Align(
      alignment: mine ? AlignmentDirectional.centerEnd : AlignmentDirectional.centerStart,
      child: ConstrainedBox(
        constraints: const BoxConstraints(maxWidth: 320),
        child: Container(
          margin: const EdgeInsets.symmetric(vertical: 4),
          padding: const EdgeInsets.symmetric(horizontal: 12, vertical: 8),
          decoration: BoxDecoration(
            color: mine ? scheme.primaryContainer : scheme.surfaceContainerHighest,
            borderRadius: BorderRadius.circular(16),
          ),
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.start,
            mainAxisSize: MainAxisSize.min,
            children: [
              if (!mine) Text(message.name, style: theme.textTheme.labelMedium?.copyWith(color: scheme.primary)),
              // A message keeps its own direction, whatever the app's language.
              Text(
                message.text,
                textDirection: Bidi.detectRtlDirectionality(message.text) ? TextDirection.rtl : TextDirection.ltr,
              ),
              if (time.isNotEmpty)
                Align(
                  alignment: AlignmentDirectional.centerEnd,
                  child: Text(time, style: theme.textTheme.labelSmall?.copyWith(color: scheme.onSurfaceVariant)),
                ),
            ],
          ),
        ),
      ),
    );
  }
}
