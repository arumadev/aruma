import 'package:flutter/foundation.dart';

/// The signed-in user, as Auth knows them.
@immutable
class AppUser {
  const AppUser({required this.uid, required this.isGuest, this.email, this.displayName});

  final String uid;
  final bool isGuest;
  final String? email;
  final String? displayName;
}

/// users/{uid}
@immutable
class Profile {
  const Profile({required this.uid, required this.name, this.photoUrl});

  final String uid;
  final String name;
  final String? photoUrl;
}

/// A team the user belongs to: teams/{teamId} with the user's own member document.
@immutable
class TeamSummary {
  const TeamSummary({required this.id, required this.name, required this.role});

  /// The team's document id, which is also its join code.
  final String id;
  final String name;

  /// 'owner' or 'member'.
  final String role;

  bool get isOwner => role == 'owner';
}

/// teams/{teamId}
@immutable
class Team {
  const Team({required this.id, required this.name, required this.owner});

  final String id;
  final String name;
  final String owner;
}

/// teams/{teamId}/members/{uid}
@immutable
class Member {
  const Member({required this.uid, required this.name, required this.role});

  final String uid;
  final String name;
  final String role;
}

/// A task's file: Storage teams/{teamId}/tasks/{taskId}/{name}.
@immutable
class Attachment {
  const Attachment({required this.name, required this.path});

  final String name;
  final String path;
}

/// teams/{teamId}/tasks/{taskId}
@immutable
class TaskItem {
  const TaskItem({
    required this.id,
    required this.title,
    required this.done,
    required this.createdBy,
    this.assignee,
    this.createdAt,
    this.attachment,
  });

  final String id;
  final String title;
  final bool done;
  final String createdBy;
  final String? assignee;

  /// Null only for a moment: a task this client just added, before the server set its time.
  final DateTime? createdAt;
  final Attachment? attachment;
}

/// chat/{teamId}/messages/{pushId}
@immutable
class ChatMessage {
  const ChatMessage({required this.id, required this.uid, required this.name, required this.text, this.sentAt});

  final String id;
  final String uid;
  final String name;
  final String text;
  final DateTime? sentAt;
}

/// presence/{teamId}/{uid}
@immutable
class PresenceEntry {
  const PresenceEntry({required this.uid, required this.name});

  final String uid;
  final String name;
}

/// The progress of a running upload, from 0 to 1.
typedef ProgressCallback = void Function(double fraction);
