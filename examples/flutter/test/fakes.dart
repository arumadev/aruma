import 'dart:async';
import 'dart:typed_data';

import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:shared_preferences/shared_preferences.dart';
import 'package:team_board/src/app.dart';
import 'package:team_board/src/data/models.dart';
import 'package:team_board/src/data/repositories.dart';
import 'package:team_board/src/device.dart';
import 'package:team_board/src/settings.dart';

/// The repositories in memory, with the rules the screens depend on: wrong and weak passwords,
/// unknown join codes, and deleting another member's task refused as by the security rules.
class FakeStore {
  final users = <String, ({String email, String password, String name})>{};
  final profiles = <String, Profile>{};
  final teams = <String, Team>{};
  final members = <String, Map<String, Member>>{};
  final tasks = <String, List<TaskItem>>{};
  final messages = <String, List<ChatMessage>>{};
  final presence = <String, Map<String, PresenceEntry>>{};
  final connected = StreamController<bool>.broadcast();
  bool online = true;
  final _changes = StreamController<void>.broadcast();
  AppUser? user;
  final _users = StreamController<AppUser?>.broadcast();
  int _ids = 0;

  /// The last attachment link opened.
  Uri? opened;

  /// The file the fake picker returns next.
  PickedFile? nextFile;

  String nextId() => 'id${++_ids}';

  void changed() => _changes.add(null);

  void setUser(AppUser? u) {
    user = u;
    _users.add(u);
    changed();
  }

  void setOnline(bool value) {
    online = value;
    connected.add(value);
  }

  /// A stream that emits [read] now and after every change.
  Stream<T> live<T>(T Function() read) async* {
    yield read();
    await for (final _ in _changes.stream) {
      yield read();
    }
  }

  Backend backend() => Backend(
        auth: _Auth(this),
        profiles: _Profiles(this),
        teams: _Teams(this),
        tasks: _Tasks(this),
        chat: _Chat(this),
        presence: _Presence(this),
      );

  /// A signed-in user with a profile, for tests that start past the sign-in screen.
  AppUser signedIn(String uid, String name, {String? email}) {
    users[uid] = (email: email ?? '$uid@example.com', password: 'secret1', name: name);
    profiles[uid] = Profile(uid: uid, name: name);
    final u = AppUser(uid: uid, isGuest: false, email: email ?? '$uid@example.com', displayName: name);
    setUser(u);
    return u;
  }

  /// A team with [ownerUid] as owner and [memberUids] as members.
  void team(String id, String name, String ownerUid, {List<String> memberUids = const []}) {
    teams[id] = Team(id: id, name: name, owner: ownerUid);
    members[id] = {
      ownerUid: Member(uid: ownerUid, name: profiles[ownerUid]?.name ?? ownerUid, role: 'owner'),
      for (final m in memberUids) m: Member(uid: m, name: profiles[m]?.name ?? m, role: 'member'),
    };
    tasks[id] = [];
    messages[id] = [];
    changed();
  }
}

class FakeDevice extends Device {
  const FakeDevice(this.store);

  final FakeStore store;

  @override
  Future<PickedFile?> pickFile({bool images = false}) async => store.nextFile;

  @override
  Future<bool> open(Uri url) async {
    store.opened = url;
    return true;
  }
}

class _Auth implements AuthRepository {
  _Auth(this.s);

  final FakeStore s;

  @override
  Stream<AppUser?> userChanges() async* {
    yield s.user;
    yield* s._users.stream;
  }

  @override
  AppUser? get currentUser => s.user;

  @override
  Future<void> signIn({required String email, required String password}) async {
    for (final e in s.users.entries) {
      if (e.value.email == email) {
        if (e.value.password != password) throw const AppException(AppErrorKind.wrongPassword);
        s.setUser(AppUser(uid: e.key, isGuest: false, email: email, displayName: e.value.name));
        return;
      }
    }
    throw const AppException(AppErrorKind.wrongPassword);
  }

  @override
  Future<void> signUp({required String name, required String email, required String password}) async {
    if (password.length < 6) throw const AppException(AppErrorKind.weakPassword);
    if (s.users.values.any((u) => u.email == email)) throw const AppException(AppErrorKind.emailInUse);
    final uid = s.nextId();
    s.users[uid] = (email: email, password: password, name: name);
    s.profiles[uid] = Profile(uid: uid, name: name);
    s.setUser(AppUser(uid: uid, isGuest: false, email: email, displayName: name));
  }

  @override
  Future<void> signInAsGuest({required String name}) async {
    final uid = s.nextId();
    s.profiles[uid] = Profile(uid: uid, name: name);
    s.setUser(AppUser(uid: uid, isGuest: true, displayName: name));
  }

  @override
  Future<void> signOut() async => s.setUser(null);
}

class _Profiles implements ProfileRepository {
  _Profiles(this.s);

  final FakeStore s;

  @override
  Stream<Profile?> watch(String uid) => s.live(() => s.profiles[uid]);

  @override
  Future<void> saveName(String uid, String name) async {
    s.profiles[uid] = Profile(uid: uid, name: name, photoUrl: s.profiles[uid]?.photoUrl);
    s.changed();
  }

  @override
  Future<void> uploadAvatar(String uid, String name, Uint8List bytes, String contentType, ProgressCallback onProgress) async {
    onProgress(0.5);
    await Future<void>.delayed(const Duration(milliseconds: 10));
    onProgress(1);
    // An empty URL: the Avatar shows the initial (no network in widget tests).
    s.profiles[uid] = Profile(uid: uid, name: name, photoUrl: '');
    s.changed();
  }

  @override
  Future<void> removeAvatar(String uid, String name, String photoUrl) async {
    s.profiles[uid] = Profile(uid: uid, name: name);
    s.changed();
  }
}

class _Teams implements TeamRepository {
  _Teams(this.s);

  final FakeStore s;

  @override
  Stream<List<TeamSummary>> watchMyTeams(String uid) => s.live(() => [
        for (final e in s.members.entries)
          if (e.value[uid] case final Member m) TeamSummary(id: e.key, name: s.teams[e.key]!.name, role: m.role),
      ]);

  @override
  Future<String> createTeam({required String name, required String uid, required String userName}) async {
    final code = 'CODE${s.teams.length + 1}';
    s.teams[code] = Team(id: code, name: name, owner: uid);
    s.members[code] = {uid: Member(uid: uid, name: userName, role: 'owner')};
    s.tasks[code] = [];
    s.messages[code] = [];
    s.changed();
    return code;
  }

  @override
  Future<TeamSummary> joinTeam({required String code, required String uid, required String userName}) async {
    final team = s.teams[code];
    if (team == null) throw const AppException(AppErrorKind.teamNotFound);
    s.members[code]![uid] = Member(uid: uid, name: userName, role: 'member');
    s.changed();
    return TeamSummary(id: code, name: team.name, role: 'member');
  }

  @override
  Stream<Team?> watchTeam(String teamId) => s.live(() => s.teams[teamId]);

  @override
  Stream<List<Member>> watchMembers(String teamId) => s.live(() => [...?s.members[teamId]?.values]);
}

class _Tasks implements TaskRepository {
  _Tasks(this.s);

  final FakeStore s;

  void _replace(String teamId, String id, TaskItem Function(TaskItem) f) {
    final list = s.tasks[teamId]!;
    final i = list.indexWhere((t) => t.id == id);
    list[i] = f(list[i]);
    s.changed();
  }

  @override
  Stream<List<TaskItem>> watchTasks(String teamId) => s.live(() => [...s.tasks[teamId]!.reversed]);

  @override
  Future<void> addTask(String teamId, {required String title, required String uid}) async {
    s.tasks[teamId]!.add(TaskItem(id: s.nextId(), title: title, done: false, createdBy: uid, createdAt: DateTime.now()));
    s.changed();
  }

  @override
  Future<void> setDone(String teamId, String taskId, bool done) async => _replace(
      teamId,
      taskId,
      (t) => TaskItem(
          id: t.id, title: t.title, done: done, createdBy: t.createdBy, assignee: t.assignee, attachment: t.attachment));

  @override
  Future<void> setAssignee(String teamId, String taskId, String? uid) async => _replace(
      teamId,
      taskId,
      (t) => TaskItem(
          id: t.id, title: t.title, done: t.done, createdBy: t.createdBy, assignee: uid, attachment: t.attachment));

  @override
  Future<void> deleteTask(String teamId, TaskItem task) async {
    final me = s.user!.uid;
    // firestore.rules: the task's author or the team's owner.
    if (task.createdBy != me && s.teams[teamId]!.owner != me) {
      throw const AppException(AppErrorKind.permissionDenied);
    }
    s.tasks[teamId]!.removeWhere((t) => t.id == task.id);
    s.changed();
  }

  @override
  Future<void> attachFile(String teamId, TaskItem task,
      {required String name, required Uint8List bytes, required String contentType, required ProgressCallback onProgress}) async {
    onProgress(0.25);
    await Future<void>.delayed(const Duration(milliseconds: 10));
    onProgress(1);
    _replace(
        teamId,
        task.id,
        (t) => TaskItem(
            id: t.id,
            title: t.title,
            done: t.done,
            createdBy: t.createdBy,
            assignee: t.assignee,
            attachment: Attachment(name: name, path: 'teams/$teamId/tasks/${t.id}/$name')));
  }

  @override
  Future<Uri> attachmentUrl(Attachment attachment) async => Uri.parse('https://files.example/${attachment.path}');
}

class _Chat implements ChatRepository {
  _Chat(this.s);

  final FakeStore s;

  @override
  Stream<List<ChatMessage>> watchMessages(String teamId) => s.live(() => [...s.messages[teamId]!]);

  @override
  Future<void> send(String teamId, {required String uid, required String name, required String text}) async {
    s.messages[teamId]!.add(ChatMessage(id: s.nextId(), uid: uid, name: name, text: text, sentAt: DateTime(2026, 1, 1, 9, 30)));
    s.changed();
  }
}

class _Presence implements PresenceRepository {
  _Presence(this.s);

  final FakeStore s;

  @override
  Stream<List<PresenceEntry>> watchOnline(String teamId) => s.live(() => [...?s.presence[teamId]?.values]);

  @override
  PresenceHandle enter(String teamId, {required String uid, required String name}) {
    (s.presence[teamId] ??= {})[uid] = PresenceEntry(uid: uid, name: name);
    s.changed();
    return _Handle(s, teamId, uid);
  }

  @override
  Stream<bool> connected() async* {
    yield s.online;
    yield* s.connected.stream;
  }
}

class _Handle implements PresenceHandle {
  _Handle(this.s, this.teamId, this.uid);

  final FakeStore s;
  final String teamId;
  final String uid;

  @override
  Future<void> close() async {
    s.presence[teamId]?.remove(uid);
    s.changed();
  }
}

/// Pumps the app on [store], in [locale] when given.
Future<void> pumpApp(WidgetTester tester, FakeStore store, {String? locale, Size size = const Size(412, 915)}) async {
  tester.view.physicalSize = size * 2;
  tester.view.devicePixelRatio = 2;
  addTearDown(tester.view.reset);
  SharedPreferences.setMockInitialValues({'locale': ?locale});
  final settings = SettingsController(await SharedPreferences.getInstance());
  await tester.pumpWidget(TeamBoardApp(backend: store.backend(), settings: settings, device: FakeDevice(store)));
  await tester.pumpAndSettle();
}
