// ignore: unused_import
import 'package:intl/intl.dart' as intl;
import 'app_localizations.dart';

// ignore_for_file: type=lint

/// The translations for English (`en`).
class AppLocalizationsEn extends AppLocalizations {
  AppLocalizationsEn([String locale = 'en']) : super(locale);

  @override
  String get appTitle => 'Team Board';

  @override
  String get signInTitle => 'Sign in';

  @override
  String get signUpTitle => 'Create an account';

  @override
  String get nameLabel => 'Name';

  @override
  String get emailLabel => 'Email';

  @override
  String get passwordLabel => 'Password';

  @override
  String get signInButton => 'Sign in';

  @override
  String get signUpButton => 'Create account';

  @override
  String get switchToSignUp => 'No account? Create one';

  @override
  String get switchToSignIn => 'Have an account? Sign in';

  @override
  String get guestButton => 'Continue as guest';

  @override
  String guestName(String code) {
    return 'Guest $code';
  }

  @override
  String get nameRequired => 'Enter your name';

  @override
  String nameTooLong(int max) {
    return 'At most $max characters';
  }

  @override
  String get emailInvalid => 'Enter a valid email address';

  @override
  String get passwordRequired => 'Enter your password';

  @override
  String get errorWrongPassword => 'Wrong email or password.';

  @override
  String get errorWeakPassword =>
      'This password is too weak. Use at least 6 characters.';

  @override
  String get errorEmailInUse => 'An account with this email already exists.';

  @override
  String get errorInvalidEmail => 'That email address is not valid.';

  @override
  String get errorUserNotFound => 'There is no account with this email.';

  @override
  String get errorTooManyRequests => 'Too many attempts. Try again later.';

  @override
  String get errorPermissionDenied => 'You don\'t have permission to do that.';

  @override
  String get errorTeamNotFound => 'No team has this code.';

  @override
  String errorFileTooLarge(int max) {
    return 'The file is too large (at most $max MB).';
  }

  @override
  String get errorNotAnImage => 'Choose an image file.';

  @override
  String get errorNetwork => 'Can\'t reach the server. Check your connection.';

  @override
  String errorUnknown(String detail) {
    return 'Something went wrong: $detail';
  }

  @override
  String get offlineBanner =>
      'You are offline. Changes sync when you reconnect.';

  @override
  String get myTeams => 'My teams';

  @override
  String get noTeams => 'No teams yet';

  @override
  String get noTeamsHint =>
      'Create a team, or join one with the code its owner gives you.';

  @override
  String get createTeam => 'Create team';

  @override
  String get joinTeam => 'Join team';

  @override
  String get teamNameLabel => 'Team name';

  @override
  String get joinCodeLabel => 'Join code';

  @override
  String get create => 'Create';

  @override
  String get join => 'Join';

  @override
  String get cancel => 'Cancel';

  @override
  String teamCreated(String code) {
    return 'Team created. Its join code is $code.';
  }

  @override
  String joinedTeam(String name) {
    return 'You joined $name.';
  }

  @override
  String get ownerBadge => 'Owner';

  @override
  String get profile => 'Profile';

  @override
  String get tasksTab => 'Tasks';

  @override
  String get chatTab => 'Chat';

  @override
  String joinCodeTooltip(String code) {
    return 'Join code $code. Tap to copy.';
  }

  @override
  String get codeCopied => 'Join code copied';

  @override
  String get newTaskHint => 'New task';

  @override
  String get addTask => 'Add task';

  @override
  String get noTasks => 'No tasks yet';

  @override
  String get noTasksHint => 'Add the first one above.';

  @override
  String taskDoneLabel(String title) {
    return 'Done: $title';
  }

  @override
  String get deleteTask => 'Delete';

  @override
  String get attachFile => 'Attach a file';

  @override
  String get replaceFile => 'Replace the file';

  @override
  String openAttachment(String name) {
    return 'Open $name';
  }

  @override
  String get assign => 'Assign';

  @override
  String get unassigned => 'Nobody';

  @override
  String assignedTo(String name) {
    return 'Assigned to $name';
  }

  @override
  String uploading(int percent) {
    return 'Uploading $percent%';
  }

  @override
  String get taskDeleted => 'Task deleted';

  @override
  String get fileAttached => 'File attached';

  @override
  String get couldNotOpen => 'Could not open the file.';

  @override
  String get openTasks => 'Open';

  @override
  String get doneTasks => 'Done';

  @override
  String taskMenu(String title) {
    return 'More for $title';
  }

  @override
  String get messageHint => 'Message';

  @override
  String get send => 'Send';

  @override
  String get noMessages => 'No messages yet';

  @override
  String get noMessagesHint => 'Say hello to your team.';

  @override
  String onlineNow(String names) {
    return 'Online: $names';
  }

  @override
  String get listSeparator => ', ';

  @override
  String get you => 'you';

  @override
  String get save => 'Save';

  @override
  String get saved => 'Saved';

  @override
  String get changePicture => 'Change picture';

  @override
  String get removePicture => 'Remove picture';

  @override
  String get language => 'Language';

  @override
  String get languageDevice => 'Device language';

  @override
  String get languageEnglish => 'English';

  @override
  String get languageArabic => 'Arabic';

  @override
  String get theme => 'Theme';

  @override
  String get themeSystem => 'System';

  @override
  String get themeLight => 'Light';

  @override
  String get themeDark => 'Dark';

  @override
  String get signOut => 'Sign out';

  @override
  String signedInAs(String email) {
    return 'Signed in as $email';
  }

  @override
  String get guestAccount =>
      'Guest account: create an account to keep your data on other devices.';

  @override
  String get retry => 'Retry';

  @override
  String get loading => 'Loading';

  @override
  String get members => 'Members';
}
