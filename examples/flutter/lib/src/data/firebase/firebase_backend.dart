import 'dart:async';
import 'dart:math';
import 'dart:typed_data';

import 'package:aruma_storage/aruma_storage.dart';
import 'package:cloud_firestore/cloud_firestore.dart';
import 'package:firebase_auth/firebase_auth.dart';
import 'package:firebase_database/firebase_database.dart';
import 'package:firebase_storage/firebase_storage.dart';

import '../../config.dart';
import '../models.dart';
import '../repositories.dart';
import 'errors.dart';

/// The repositories on the FlutterFire plugins, in their production mode: no useEmulator call
/// anywhere. On Aruma only two lines differ from a plain Firebase app, both behind
/// [AppConfig.useAruma]: Firestore's host (lib/main.dart) and [fromArumaUrl] below.
Backend firebaseBackend(AppConfig config) {
  final auth = FirebaseAuth.instance;
  final db = FirebaseFirestore.instance;
  final rtdb = FirebaseDatabase.instance;
  final storage = FirebaseStorage.instance;
  return Backend(
    auth: _Auth(auth, db),
    profiles: _Profiles(config, auth, db, storage),
    teams: _Teams(db, rtdb),
    tasks: _Tasks(db, storage),
    chat: _Chat(rtdb),
    presence: _Presence(rtdb),
  );
}

AppUser? _user(User? u) => u == null
    ? null
    : AppUser(uid: u.uid, isGuest: u.isAnonymous, email: u.email, displayName: u.displayName);

Map<String, Object?> _profile(String name) =>
    {'name': name, 'updatedAt': FieldValue.serverTimestamp()};

class _Auth implements AuthRepository {
  _Auth(this._auth, this._db);

  final FirebaseAuth _auth;
  final FirebaseFirestore _db;

  @override
  Stream<AppUser?> userChanges() => _auth.userChanges().map(_user);

  @override
  AppUser? get currentUser => _user(_auth.currentUser);

  @override
  Future<void> signIn({required String email, required String password}) => guarded(() async {
        await _auth.signInWithEmailAndPassword(email: email, password: password);
      });

  @override
  Future<void> signUp({required String name, required String email, required String password}) =>
      guarded(() async {
        final cred = await _auth.createUserWithEmailAndPassword(email: email, password: password);
        await _named(cred.user!, name);
      });

  @override
  Future<void> signInAsGuest({required String name}) => guarded(() async {
        final cred = await _auth.signInAnonymously();
        await _named(cred.user!, name);
      });

  Future<void> _named(User user, String name) async {
    await _db.doc('users/${user.uid}').set({..._profile(name), 'photoURL': null});
    await user.updateDisplayName(name);
  }

  @override
  Future<void> signOut() => guarded(_auth.signOut);
}

class _Profiles implements ProfileRepository {
  _Profiles(this._config, this._auth, this._db, this._storage);

  final AppConfig _config;
  final FirebaseAuth _auth;
  final FirebaseFirestore _db;
  final FirebaseStorage _storage;

  @override
  Stream<Profile?> watch(String uid) => guardedStream(_db.doc('users/$uid').snapshots().map((s) {
        final d = s.data();
        if (d == null) return null;
        return Profile(uid: uid, name: d['name'] as String? ?? '', photoUrl: d['photoURL'] as String?);
      }));

  @override
  Future<void> saveName(String uid, String name) => guarded(() async {
        await _db.doc('users/$uid').set(_profile(name), SetOptions(merge: true));
        await _auth.currentUser?.updateDisplayName(name);
      });

  @override
  Future<void> uploadAvatar(String uid, String name, Uint8List bytes, String contentType, ProgressCallback onProgress) =>
      guarded(() async {
        final ref = _storage.ref('avatars/$uid/avatar');
        await _upload(ref.putData(bytes, SettableMetadata(contentType: contentType)), onProgress);
        final url = await ref.getDownloadURL();
        await _db.doc('users/$uid').set({..._profile(name), 'photoURL': url}, SetOptions(merge: true));
      });

  @override
  Future<void> removeAvatar(String uid, String name, String photoUrl) => guarded(() async {
        // users/{uid}.photoURL holds a download link. firebase_storage parses http(s) links in
        // Dart and takes only Google's hosts, so on Aruma the link is first turned into its
        // gs:// form (sdk/flutter/aruma_storage).
        final ref = _storage.refFromURL(_config.useAruma ? fromArumaUrl(photoUrl) : photoUrl);
        await ref.delete();
        await _db.doc('users/$uid').set({..._profile(name), 'photoURL': null}, SetOptions(merge: true));
      });
}

Future<void> _upload(UploadTask task, ProgressCallback onProgress) async {
  final sub = task.snapshotEvents.listen((s) {
    if (s.totalBytes > 0) onProgress(s.bytesTransferred / s.totalBytes);
  }, onError: (Object _) {});
  try {
    await task;
    onProgress(1);
  } finally {
    await sub.cancel();
  }
}

// Join codes: no 0/O or 1/I/L, so that they can be read out and typed.
const _codeAlphabet = 'ABCDEFGHJKMNPQRSTUVWXYZ23456789';
final _random = Random.secure();
String _newCode() => List.generate(6, (_) => _codeAlphabet[_random.nextInt(_codeAlphabet.length)]).join();

class _Teams implements TeamRepository {
  _Teams(this._db, this._rtdb);

  final FirebaseFirestore _db;
  final FirebaseDatabase _rtdb;

  @override
  Stream<List<TeamSummary>> watchMyTeams(String uid) {
    final names = <String, String>{};
    final query = _db.collectionGroup('members').where('uid', isEqualTo: uid);
    return guardedStream(query.snapshots().asyncMap((snap) async {
      final teams = <TeamSummary>[];
      for (final m in snap.docs) {
        final teamRef = m.reference.parent.parent!;
        var name = names[teamRef.id];
        if (name == null) {
          final team = await teamRef.get();
          name = names[teamRef.id] = team.data()?['name'] as String? ?? teamRef.id;
        }
        teams.add(TeamSummary(id: teamRef.id, name: name, role: m.data()['role'] as String? ?? 'member'));
      }
      teams.sort((a, b) => a.name.toLowerCase().compareTo(b.name.toLowerCase()));
      return teams;
    }));
  }

  @override
  Future<String> createTeam({required String name, required String uid, required String userName}) =>
      guarded(() async {
        var code = _newCode();
        // The code is the team's id: pick one that is free (a set on an existing team would be an
        // update, which the rules give to its owner only).
        while ((await _db.doc('teams/$code').get()).exists) {
          code = _newCode();
        }
        final batch = _db.batch()
          ..set(_db.doc('teams/$code'), {'name': name, 'owner': uid, 'createdAt': FieldValue.serverTimestamp()})
          ..set(_db.doc('teams/$code/members/$uid'),
              {'uid': uid, 'role': 'owner', 'name': userName, 'joinedAt': FieldValue.serverTimestamp()});
        await batch.commit();
        await _rtdb.ref('members/$code/$uid').set(true);
        return code;
      });

  @override
  Future<TeamSummary> joinTeam({required String code, required String uid, required String userName}) =>
      guarded(() async {
        final team = await _db.doc('teams/$code').get();
        if (!team.exists) throw const AppException(AppErrorKind.teamNotFound);
        final name = team.data()?['name'] as String? ?? code;
        final member = _db.doc('teams/$code/members/$uid');
        // A member reads their own document; anyone else is refused, so a refusal means "not yet".
        var joined = false;
        try {
          joined = (await member.get()).exists;
        } on FirebaseException catch (e) {
          if (e.code != 'permission-denied') rethrow;
        }
        if (!joined) {
          await member.set({'uid': uid, 'role': 'member', 'name': userName, 'joinedAt': FieldValue.serverTimestamp()});
        }
        await _rtdb.ref('members/$code/$uid').set(true);
        return TeamSummary(id: code, name: name, role: 'member');
      });

  @override
  Stream<Team?> watchTeam(String teamId) => guardedStream(_db.doc('teams/$teamId').snapshots().map((s) {
        final d = s.data();
        if (d == null) return null;
        return Team(id: s.id, name: d['name'] as String? ?? s.id, owner: d['owner'] as String? ?? '');
      }));

  @override
  Stream<List<Member>> watchMembers(String teamId) =>
      guardedStream(_db.collection('teams/$teamId/members').snapshots().map((s) => [
            for (final d in s.docs)
              Member(uid: d.id, name: d.data()['name'] as String? ?? '', role: d.data()['role'] as String? ?? 'member'),
          ]));
}

class _Tasks implements TaskRepository {
  _Tasks(this._db, this._storage);

  final FirebaseFirestore _db;
  final FirebaseStorage _storage;

  CollectionReference<Map<String, dynamic>> _col(String teamId) => _db.collection('teams/$teamId/tasks');

  @override
  Stream<List<TaskItem>> watchTasks(String teamId) =>
      guardedStream(_col(teamId).orderBy('createdAt', descending: true).snapshots().map((s) => [
            for (final d in s.docs) _task(d.id, d.data()),
          ]));

  TaskItem _task(String id, Map<String, dynamic> d) {
    final a = d['attachment'];
    return TaskItem(
      id: id,
      title: d['title'] as String? ?? '',
      done: d['done'] == true,
      createdBy: d['createdBy'] as String? ?? '',
      assignee: d['assignee'] as String?,
      createdAt: (d['createdAt'] as Timestamp?)?.toDate(),
      attachment: a is Map && a['path'] is String
          ? Attachment(name: a['name'] as String? ?? '', path: a['path'] as String)
          : null,
    );
  }

  @override
  Future<void> addTask(String teamId, {required String title, required String uid}) => guarded(() async {
        await _col(teamId).add({
          'title': title,
          'done': false,
          'assignee': null,
          'createdBy': uid,
          'createdAt': FieldValue.serverTimestamp(),
          'updatedAt': FieldValue.serverTimestamp(),
          'attachment': null,
        });
      });

  @override
  Future<void> setDone(String teamId, String taskId, bool done) => guarded(() async {
        await _col(teamId).doc(taskId).update({'done': done, 'updatedAt': FieldValue.serverTimestamp()});
      });

  @override
  Future<void> setAssignee(String teamId, String taskId, String? uid) => guarded(() async {
        await _col(teamId).doc(taskId).update({'assignee': uid, 'updatedAt': FieldValue.serverTimestamp()});
      });

  @override
  Future<void> deleteTask(String teamId, TaskItem task) => guarded(() async {
        // The task first: a member who may not delete it is refused before the file is touched.
        await _col(teamId).doc(task.id).delete();
        final a = task.attachment;
        if (a != null) {
          try {
            await _storage.ref(a.path).delete();
          } on FirebaseException catch (e) {
            if (e.code != 'object-not-found') rethrow;
          }
        }
      });

  @override
  Future<void> attachFile(String teamId, TaskItem task,
          {required String name,
          required Uint8List bytes,
          required String contentType,
          required ProgressCallback onProgress}) =>
      guarded(() async {
        // One path segment: a '/' in the name would make a folder the rules do not match.
        final safe = name.replaceAll('/', '_');
        final path = 'teams/$teamId/tasks/${task.id}/$safe';
        await _upload(_storage.ref(path).putData(bytes, SettableMetadata(contentType: contentType)), onProgress);
        await _col(teamId).doc(task.id).update({
          'attachment': {'name': safe, 'path': path},
          'updatedAt': FieldValue.serverTimestamp(),
        });
        final old = task.attachment;
        if (old != null && old.path != path) {
          await _storage.ref(old.path).delete().catchError((Object _) {});
        }
      });

  @override
  Future<Uri> attachmentUrl(Attachment attachment) =>
      guarded(() async => Uri.parse(await _storage.ref(attachment.path).getDownloadURL()));
}

class _Chat implements ChatRepository {
  _Chat(this._rtdb);

  final FirebaseDatabase _rtdb;

  @override
  Stream<List<ChatMessage>> watchMessages(String teamId) => guardedStream(
      _rtdb.ref('chat/$teamId/messages').orderByChild('ts').limitToLast(50).onValue.map((e) => [
            for (final c in e.snapshot.children)
              if (c.value case final Map<Object?, Object?> m)
                ChatMessage(
                  id: c.key!,
                  uid: m['uid'] as String? ?? '',
                  name: m['name'] as String? ?? '',
                  text: m['text'] as String? ?? '',
                  sentAt: m['ts'] is num ? DateTime.fromMillisecondsSinceEpoch((m['ts'] as num).toInt()) : null,
                ),
          ]));

  @override
  Future<void> send(String teamId, {required String uid, required String name, required String text}) =>
      guarded(() async {
        await _rtdb.ref('chat/$teamId/messages').push().set({
          'uid': uid,
          'name': name,
          'text': text,
          'ts': ServerValue.timestamp,
        });
      });
}

class _Presence implements PresenceRepository {
  _Presence(this._rtdb);

  final FirebaseDatabase _rtdb;

  @override
  Stream<List<PresenceEntry>> watchOnline(String teamId) =>
      guardedStream(_rtdb.ref('presence/$teamId').onValue.map((e) => [
            for (final c in e.snapshot.children)
              if (c.value case final Map<Object?, Object?> m when m['online'] == true)
                PresenceEntry(uid: c.key!, name: m['name'] as String? ?? ''),
          ]));

  @override
  PresenceHandle enter(String teamId, {required String uid, required String name}) =>
      _PresenceHandle(_rtdb, teamId, uid, name);

  @override
  Stream<bool> connected() => _rtdb.ref('.info/connected').onValue.map((e) => e.snapshot.value == true);
}

class _PresenceHandle implements PresenceHandle {
  _PresenceHandle(FirebaseDatabase rtdb, String teamId, String uid, String name)
      : _ref = rtdb.ref('presence/$teamId/$uid') {
    _start(rtdb, teamId, uid, name);
  }

  final DatabaseReference _ref;
  StreamSubscription<DatabaseEvent>? _sub;
  bool _closed = false;

  Future<void> _start(FirebaseDatabase rtdb, String teamId, String uid, String name) async {
    try {
      // The Realtime Database's rules read membership from members/{teamId}/{uid}.
      await rtdb.ref('members/$teamId/$uid').set(true);
    } catch (_) {
      // Without it the presence write below is refused too; the chat shows the error.
    }
    if (_closed) return;
    _sub = rtdb.ref('.info/connected').onValue.listen((e) async {
      if (e.snapshot.value != true || _closed) return;
      try {
        // On every (re)connection: first the server-side removal, then the entry.
        await _ref.onDisconnect().remove();
        await _ref.set({'name': name, 'online': true, 'lastSeen': ServerValue.timestamp});
      } catch (_) {
        // Refused or offline: the next connection tries again.
      }
    });
  }

  @override
  Future<void> close() async {
    _closed = true;
    await _sub?.cancel();
    try {
      await _ref.onDisconnect().cancel();
      await _ref.remove();
    } catch (_) {
      // Signed out already, or offline: the server's onDisconnect removes it.
    }
  }
}
