import 'dart:typed_data';

import 'models.dart';

/// The small interfaces the screens use. lib/src/data/firebase/ implements them on the FlutterFire
/// plugins; the widget tests implement them in memory (test/fakes.dart). Every failure is an
/// [AppException], so the screens never see a plugin's own exception types.

/// Why an operation failed, in the words the screens show.
enum AppErrorKind {
  wrongPassword,
  weakPassword,
  emailInUse,
  invalidEmail,
  userNotFound,
  tooManyRequests,
  permissionDenied,
  teamNotFound,
  fileTooLarge,
  notAnImage,
  network,
  unknown,
}

class AppException implements Exception {
  const AppException(this.kind, [this.detail]);

  final AppErrorKind kind;

  /// The plugin's own code and message, for [AppErrorKind.unknown] and for the logs.
  final String? detail;

  @override
  String toString() => 'AppException(${kind.name}${detail == null ? '' : ': $detail'})';
}

abstract interface class AuthRepository {
  /// The current user, then every change: sign-in, sign-out, and the session restored at start.
  Stream<AppUser?> userChanges();

  AppUser? get currentUser;

  Future<void> signIn({required String email, required String password});

  /// Creates the account, sets its display name and writes users/{uid}.
  Future<void> signUp({required String name, required String email, required String password});

  /// Anonymous sign-in, with [name] as the guest's name.
  Future<void> signInAsGuest({required String name});

  Future<void> signOut();
}

abstract interface class ProfileRepository {
  /// users/{uid}, live. Null while the document does not exist.
  Stream<Profile?> watch(String uid);

  Future<void> saveName(String uid, String name);

  /// Uploads avatars/{uid}/avatar and stores its download URL in users/{uid}.photoURL. [name] is
  /// written with it: the rules want a name on every write of the profile.
  Future<void> uploadAvatar(String uid, String name, Uint8List bytes, String contentType, ProgressCallback onProgress);

  /// Deletes the picture named by [photoUrl] (a download link) and clears users/{uid}.photoURL.
  Future<void> removeAvatar(String uid, String name, String photoUrl);
}

abstract interface class TeamRepository {
  /// The teams the user is a member of (a collection-group query on members), live.
  Stream<List<TeamSummary>> watchMyTeams(String uid);

  /// Creates the team and the owner's member document in one batch. Returns the join code.
  Future<String> createTeam({required String name, required String uid, required String userName});

  /// Joins the team with this code. Throws [AppErrorKind.teamNotFound] for an unknown code.
  Future<TeamSummary> joinTeam({required String code, required String uid, required String userName});

  Stream<Team?> watchTeam(String teamId);

  Stream<List<Member>> watchMembers(String teamId);
}

abstract interface class TaskRepository {
  /// The team's tasks, newest first, live.
  Stream<List<TaskItem>> watchTasks(String teamId);

  Future<void> addTask(String teamId, {required String title, required String uid});

  Future<void> setDone(String teamId, String taskId, bool done);

  Future<void> setAssignee(String teamId, String taskId, String? uid);

  /// Deletes the task and its attachment.
  Future<void> deleteTask(String teamId, TaskItem task);

  /// Uploads the task's file (replacing an earlier one) and records it on the task.
  Future<void> attachFile(String teamId, TaskItem task,
      {required String name, required Uint8List bytes, required String contentType, required ProgressCallback onProgress});

  /// A link that opens the attachment.
  Future<Uri> attachmentUrl(Attachment attachment);
}

abstract interface class ChatRepository {
  /// The last 50 messages by time, oldest first, live.
  Stream<List<ChatMessage>> watchMessages(String teamId);

  Future<void> send(String teamId, {required String uid, required String name, required String text});
}

/// What the user holds open while they look at a team: their presence entry. [close] removes it.
abstract interface class PresenceHandle {
  Future<void> close();
}

abstract interface class PresenceRepository {
  /// Who is online in the team, live.
  Stream<List<PresenceEntry>> watchOnline(String teamId);

  /// Writes members/{teamId}/{uid} (the Realtime Database's copy of the membership its rules read),
  /// then keeps presence/{teamId}/{uid} set while connected, removed on disconnect.
  PresenceHandle enter(String teamId, {required String uid, required String name});

  /// Whether the Realtime Database is connected (.info/connected).
  Stream<bool> connected();
}

/// Everything the screens reach the backend through.
class Backend {
  const Backend({
    required this.auth,
    required this.profiles,
    required this.teams,
    required this.tasks,
    required this.chat,
    required this.presence,
  });

  final AuthRepository auth;
  final ProfileRepository profiles;
  final TeamRepository teams;
  final TaskRepository tasks;
  final ChatRepository chat;
  final PresenceRepository presence;
}
