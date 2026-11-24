import 'dart:typed_data';

import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:shared_preferences/shared_preferences.dart';
import 'package:team_board/src/config.dart';
import 'package:team_board/src/data/models.dart';
import 'package:team_board/src/device.dart';

import 'fakes.dart';

void main() {
  group('sign in', () {
    testWidgets('a wrong password is shown as such', (tester) async {
      final store = FakeStore()..users['u1'] = (email: 'ada@example.com', password: 'right-one', name: 'Ada');
      await pumpApp(tester, store);
      expect(find.text('Sign in'), findsWidgets);
      await tester.enterText(find.widgetWithText(TextFormField, 'Email'), 'ada@example.com');
      await tester.enterText(find.widgetWithText(TextFormField, 'Password'), 'wrong-one');
      await tester.tap(find.widgetWithText(FilledButton, 'Sign in'));
      await tester.pumpAndSettle();
      expect(find.text('Wrong email or password.'), findsOneWidget);
      expect(store.user, isNull);
    });

    testWidgets('a weak password is refused on sign-up, then the account is made', (tester) async {
      final store = FakeStore();
      await pumpApp(tester, store);
      await tester.tap(find.text('No account? Create one'));
      await tester.pumpAndSettle();
      await tester.enterText(find.widgetWithText(TextFormField, 'Name'), 'Grace');
      await tester.enterText(find.widgetWithText(TextFormField, 'Email'), 'grace@example.com');
      await tester.enterText(find.widgetWithText(TextFormField, 'Password'), '123');
      await tester.tap(find.widgetWithText(FilledButton, 'Create account'));
      await tester.pumpAndSettle();
      expect(find.text('This password is too weak. Use at least 6 characters.'), findsOneWidget);

      await tester.enterText(find.widgetWithText(TextFormField, 'Password'), 'longer-secret');
      await tester.tap(find.widgetWithText(FilledButton, 'Create account'));
      await tester.pumpAndSettle();
      expect(find.text('My teams'), findsOneWidget);
      expect(find.text('No teams yet'), findsOneWidget);
    });

    testWidgets('the form checks its fields before calling Auth', (tester) async {
      final store = FakeStore();
      await pumpApp(tester, store);
      await tester.tap(find.widgetWithText(FilledButton, 'Sign in'));
      await tester.pumpAndSettle();
      expect(find.text('Enter a valid email address'), findsOneWidget);
      expect(find.text('Enter your password'), findsOneWidget);
    });

    testWidgets('a guest gets a name and lands on the teams', (tester) async {
      final store = FakeStore();
      await pumpApp(tester, store);
      await tester.tap(find.text('Continue as guest'));
      await tester.pumpAndSettle();
      expect(store.user!.isGuest, isTrue);
      expect(store.profiles[store.user!.uid]!.name, startsWith('Guest '));
      expect(find.text('My teams'), findsOneWidget);
    });
  });

  group('teams', () {
    testWidgets('create a team, then it is listed with its code', (tester) async {
      final store = FakeStore()..signedIn('u1', 'Ada');
      await pumpApp(tester, store);
      expect(find.text('No teams yet'), findsOneWidget);
      await tester.tap(find.text('Create team'));
      await tester.pumpAndSettle();
      await tester.enterText(find.widgetWithText(TextField, 'Team name'), 'Rockets');
      await tester.tap(find.widgetWithText(FilledButton, 'Create'));
      await tester.pumpAndSettle();
      // The new team opens; back on the list it is there.
      expect(find.text('Rockets'), findsOneWidget);
      expect(find.text('CODE1'), findsOneWidget);
      await tester.pageBack();
      await tester.pumpAndSettle();
      expect(find.text('Rockets'), findsOneWidget);
      expect(find.text('Join code: CODE1'), findsOneWidget);
      expect(find.text('Owner'), findsOneWidget);
    });

    testWidgets('an unknown join code is an error in the dialog', (tester) async {
      final store = FakeStore()..signedIn('u1', 'Ada');
      await pumpApp(tester, store);
      await tester.tap(find.byTooltip('Join team'));
      await tester.pumpAndSettle();
      await tester.enterText(find.widgetWithText(TextField, 'Join code'), 'NOPE');
      await tester.tap(find.widgetWithText(FilledButton, 'Join'));
      await tester.pumpAndSettle();
      expect(find.text('No team has this code.'), findsOneWidget);
    });

    testWidgets('joining by code opens the team', (tester) async {
      final store = FakeStore()
        ..signedIn('owner', 'Olga')
        ..team('ABC234', 'Rockets', 'owner')
        ..signedIn('u2', 'Ben');
      await pumpApp(tester, store);
      await tester.tap(find.byTooltip('Join team'));
      await tester.pumpAndSettle();
      await tester.enterText(find.widgetWithText(TextField, 'Join code'), ' ABC234 ');
      await tester.tap(find.widgetWithText(FilledButton, 'Join'));
      await tester.pumpAndSettle();
      expect(store.members['ABC234']!.keys, contains('u2'));
      expect(find.text('Tasks'), findsOneWidget);
      expect(find.text('Chat'), findsOneWidget);
    });

    testWidgets('the offline banner follows the connection', (tester) async {
      final store = FakeStore()..signedIn('u1', 'Ada');
      await pumpApp(tester, store);
      expect(find.text('You are offline. Changes sync when you reconnect.'), findsNothing);
      store.setOnline(false);
      await tester.pump(const Duration(seconds: 3));
      await tester.pumpAndSettle();
      expect(find.text('You are offline. Changes sync when you reconnect.'), findsOneWidget);
      store.setOnline(true);
      await tester.pumpAndSettle();
      expect(find.text('You are offline. Changes sync when you reconnect.'), findsNothing);
    });
  });

  group('tasks', () {
    Future<FakeStore> openTeam(WidgetTester tester, {String me = 'u1'}) async {
      final store = FakeStore()
        ..signedIn('owner', 'Olga')
        ..signedIn('u1', 'Ada')
        ..team('T1', 'Rockets', 'owner', memberUids: ['u1']);
      if (me != 'u1') store.signedIn(me, me);
      await pumpApp(tester, store);
      await tester.tap(find.text('Rockets'));
      await tester.pumpAndSettle();
      return store;
    }

    testWidgets('empty, add, tick off: open tasks first', (tester) async {
      final store = await openTeam(tester);
      expect(find.text('No tasks yet'), findsOneWidget);
      for (final title in ['Write the brief', 'Book the room']) {
        await tester.enterText(find.widgetWithText(TextField, 'New task'), title);
        await tester.tap(find.byTooltip('Add task'));
        await tester.pumpAndSettle();
      }
      expect(find.text('Open (2)'), findsOneWidget);
      await tester.tap(find.bySemanticsLabel('Done: Write the brief'));
      await tester.pumpAndSettle();
      expect(store.tasks['T1']!.firstWhere((t) => t.title == 'Write the brief').done, isTrue);
      expect(find.text('Open (1)'), findsOneWidget);
      expect(find.text('Done (1)'), findsOneWidget);
      // The open one is listed above the done one.
      expect(tester.getTopLeft(find.text('Book the room')).dy, lessThan(tester.getTopLeft(find.text('Write the brief')).dy));
    });

    testWidgets('deleting another member\'s task shows the rule\'s refusal', (tester) async {
      final store = await openTeam(tester);
      store.tasks['T1']!.add(const TaskItem(id: 't9', title: 'Owner task', done: false, createdBy: 'owner'));
      store.changed();
      await tester.pumpAndSettle();
      await tester.tap(find.byTooltip('More for Owner task'));
      await tester.pumpAndSettle();
      await tester.tap(find.text('Delete'));
      await tester.pumpAndSettle();
      expect(find.text("You don't have permission to do that."), findsOneWidget);
      expect(store.tasks['T1']!.length, 1);
    });

    testWidgets('attach a file: progress, then the file opens', (tester) async {
      final store = await openTeam(tester);
      await tester.enterText(find.widgetWithText(TextField, 'New task'), 'Report');
      await tester.tap(find.byTooltip('Add task'));
      await tester.pumpAndSettle();
      store.nextFile = PickedFile(name: 'notes.txt', bytes: Uint8List.fromList([1, 2, 3]), contentType: contentTypeOf('notes.txt'));
      await tester.tap(find.byTooltip('More for Report'));
      await tester.pumpAndSettle();
      await tester.tap(find.text('Attach a file'));
      await tester.pump();
      await tester.pump();
      expect(find.text('Uploading 25%'), findsOneWidget);
      await tester.pumpAndSettle();
      expect(find.text('File attached'), findsOneWidget);
      await tester.tap(find.text('notes.txt'));
      await tester.pumpAndSettle();
      expect(store.opened.toString(), endsWith('/notes.txt'));
    });

    testWidgets('a file over 10 MB is refused before the upload', (tester) async {
      final store = await openTeam(tester);
      await tester.enterText(find.widgetWithText(TextField, 'New task'), 'Video');
      await tester.tap(find.byTooltip('Add task'));
      await tester.pumpAndSettle();
      store.nextFile = PickedFile(name: 'big.bin', bytes: Uint8List(10 * 1024 * 1024), contentType: 'application/octet-stream');
      await tester.tap(find.byTooltip('More for Video'));
      await tester.pumpAndSettle();
      await tester.tap(find.text('Attach a file'));
      await tester.pumpAndSettle();
      expect(find.text('The file is too large (at most 10 MB).'), findsOneWidget);
    });

    testWidgets('assign a task to a member', (tester) async {
      await openTeam(tester);
      await tester.enterText(find.widgetWithText(TextField, 'New task'), 'Plan');
      await tester.tap(find.byTooltip('Add task'));
      await tester.pumpAndSettle();
      await tester.tap(find.byTooltip('More for Plan'));
      await tester.pumpAndSettle();
      await tester.tap(find.text('Assign'));
      await tester.pumpAndSettle();
      await tester.tap(find.text('Olga'));
      await tester.pumpAndSettle();
      expect(find.text('Assigned to Olga'), findsOneWidget);
    });
  });

  group('chat', () {
    testWidgets('messages in order, and who is online', (tester) async {
      final store = FakeStore()
        ..signedIn('owner', 'Olga')
        ..signedIn('u1', 'Ada')
        ..team('T1', 'Rockets', 'owner', memberUids: ['u1']);
      await pumpApp(tester, store);
      await tester.tap(find.text('Rockets'));
      await tester.pumpAndSettle();
      expect(store.presence['T1']!.keys, ['u1']);
      await tester.tap(find.text('Chat'));
      await tester.pumpAndSettle();
      expect(find.text('No messages yet'), findsOneWidget);
      expect(find.text('Online: Ada (you)'), findsOneWidget);
      for (final m in ['first', 'second']) {
        await tester.enterText(find.widgetWithText(TextField, 'Message'), m);
        await tester.tap(find.byTooltip('Send'));
        await tester.pumpAndSettle();
      }
      expect(tester.getTopLeft(find.text('first')).dy, lessThan(tester.getTopLeft(find.text('second')).dy));
      // Leaving the team removes the presence entry: the listeners are gone with the screen.
      await tester.pageBack();
      await tester.pumpAndSettle();
      expect(store.presence['T1'], isEmpty);
    });
  });

  group('profile and settings', () {
    testWidgets('Arabic turns the app right to left, and is remembered', (tester) async {
      final store = FakeStore()..signedIn('u1', 'Ada');
      await pumpApp(tester, store);
      await tester.tap(find.byTooltip('Profile'));
      await tester.pumpAndSettle();
      await tester.tap(find.text('Arabic'));
      await tester.pumpAndSettle();
      expect(find.text('الملف الشخصي'), findsOneWidget);
      expect(Directionality.of(tester.element(find.text('الملف الشخصي'))), TextDirection.rtl);

      // Remembered, and a new start of the app reads it back.
      expect((await SharedPreferences.getInstance()).getString('locale'), 'ar');
      await tester.pumpWidget(const SizedBox());
      await pumpApp(tester, store, locale: 'ar');
      expect(find.text('فرقي'), findsOneWidget);
    });

    testWidgets('dark theme, name saved, picture upload, sign out', (tester) async {
      final store = FakeStore()..signedIn('u1', 'Ada');
      await pumpApp(tester, store);
      await tester.tap(find.byTooltip('Profile'));
      await tester.pumpAndSettle();
      await tester.tap(find.text('Dark'));
      await tester.pumpAndSettle();
      expect(Theme.of(tester.element(find.text('Profile').first)).brightness, Brightness.dark);

      await tester.enterText(find.widgetWithText(TextFormField, 'Name'), 'Ada L.');
      await tester.tap(find.widgetWithText(FilledButton, 'Save'));
      await tester.pumpAndSettle();
      expect(store.profiles['u1']!.name, 'Ada L.');

      store.nextFile = PickedFile(name: 'me.txt', bytes: Uint8List(3), contentType: 'text/plain');
      await tester.tap(find.text('Change picture'));
      await tester.pumpAndSettle();
      expect(find.text('Choose an image file.'), findsOneWidget);
      store.nextFile = PickedFile(name: 'me.png', bytes: Uint8List(3), contentType: 'image/png');
      await tester.tap(find.text('Change picture'));
      await tester.pumpAndSettle();
      expect(find.text('Remove picture'), findsOneWidget);

      await tester.tap(find.text('Sign out'));
      await tester.pumpAndSettle();
      expect(store.user, isNull);
      expect(find.text('Continue as guest'), findsOneWidget);
    });
  });

  group('aruma.json', () {
    test('on: the URL gives Firestore its host', () {
      final c = AppConfig.parse('{"useAruma": true, "arumaUrl": "http://127.0.0.1:8080/"}');
      expect(c.firestoreHost, '127.0.0.1:8080');
      expect(c.firestoreSsl, isFalse);
      final https = AppConfig.parse('{"useAruma": true, "arumaUrl": "https://aruma.example.com"}');
      expect(https.firestoreHost, 'aruma.example.com');
      expect(https.firestoreSsl, isTrue);
    });

    test('off: no URL needed', () {
      expect(AppConfig.parse('{"useAruma": false}').useAruma, isFalse);
    });

    test('a URL with a path, or none, is refused', () {
      expect(() => AppConfig.parse('{"useAruma": true, "arumaUrl": "https://a.example/x"}'), throwsFormatException);
      expect(() => AppConfig.parse('{"useAruma": true}'), throwsFormatException);
      expect(() => AppConfig.parse('{"arumaUrl": "https://a.example"}'), throwsFormatException);
    });
  });
}
