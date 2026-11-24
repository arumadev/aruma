import 'dart:async';

import 'package:flutter/foundation.dart';
import 'package:flutter/widgets.dart';
import 'package:flutter_localizations/flutter_localizations.dart';
import 'package:intl/intl.dart' as intl;

import 'app_localizations_ar.dart';
import 'app_localizations_en.dart';

// ignore_for_file: type=lint

/// Callers can lookup localized strings with an instance of AppLocalizations
/// returned by `AppLocalizations.of(context)`.
///
/// Applications need to include `AppLocalizations.delegate()` in their app's
/// `localizationDelegates` list, and the locales they support in the app's
/// `supportedLocales` list. For example:
///
/// ```dart
/// import 'l10n/app_localizations.dart';
///
/// return MaterialApp(
///   localizationsDelegates: AppLocalizations.localizationsDelegates,
///   supportedLocales: AppLocalizations.supportedLocales,
///   home: MyApplicationHome(),
/// );
/// ```
///
/// ## Update pubspec.yaml
///
/// Please make sure to update your pubspec.yaml to include the following
/// packages:
///
/// ```yaml
/// dependencies:
///   # Internationalization support.
///   flutter_localizations:
///     sdk: flutter
///   intl: any # Use the pinned version from flutter_localizations
///
///   # Rest of dependencies
/// ```
///
/// ## iOS Applications
///
/// iOS applications define key application metadata, including supported
/// locales, in an Info.plist file that is built into the application bundle.
/// To configure the locales supported by your app, you’ll need to edit this
/// file.
///
/// First, open your project’s ios/Runner.xcworkspace Xcode workspace file.
/// Then, in the Project Navigator, open the Info.plist file under the Runner
/// project’s Runner folder.
///
/// Next, select the Information Property List item, select Add Item from the
/// Editor menu, then select Localizations from the pop-up menu.
///
/// Select and expand the newly-created Localizations item then, for each
/// locale your application supports, add a new item and select the locale
/// you wish to add from the pop-up menu in the Value field. This list should
/// be consistent with the languages listed in the AppLocalizations.supportedLocales
/// property.
abstract class AppLocalizations {
  AppLocalizations(String locale)
    : localeName = intl.Intl.canonicalizedLocale(locale.toString());

  final String localeName;

  static AppLocalizations of(BuildContext context) {
    return Localizations.of<AppLocalizations>(context, AppLocalizations)!;
  }

  static const LocalizationsDelegate<AppLocalizations> delegate =
      _AppLocalizationsDelegate();

  /// A list of this localizations delegate along with the default localizations
  /// delegates.
  ///
  /// Returns a list of localizations delegates containing this delegate along with
  /// GlobalMaterialLocalizations.delegate, GlobalCupertinoLocalizations.delegate,
  /// and GlobalWidgetsLocalizations.delegate.
  ///
  /// Additional delegates can be added by appending to this list in
  /// MaterialApp. This list does not have to be used at all if a custom list
  /// of delegates is preferred or required.
  static const List<LocalizationsDelegate<dynamic>> localizationsDelegates =
      <LocalizationsDelegate<dynamic>>[
        delegate,
        GlobalMaterialLocalizations.delegate,
        GlobalCupertinoLocalizations.delegate,
        GlobalWidgetsLocalizations.delegate,
      ];

  /// A list of this localizations delegate's supported locales.
  static const List<Locale> supportedLocales = <Locale>[
    Locale('ar'),
    Locale('en'),
  ];

  /// No description provided for @appTitle.
  ///
  /// In en, this message translates to:
  /// **'Team Board'**
  String get appTitle;

  /// No description provided for @signInTitle.
  ///
  /// In en, this message translates to:
  /// **'Sign in'**
  String get signInTitle;

  /// No description provided for @signUpTitle.
  ///
  /// In en, this message translates to:
  /// **'Create an account'**
  String get signUpTitle;

  /// No description provided for @nameLabel.
  ///
  /// In en, this message translates to:
  /// **'Name'**
  String get nameLabel;

  /// No description provided for @emailLabel.
  ///
  /// In en, this message translates to:
  /// **'Email'**
  String get emailLabel;

  /// No description provided for @passwordLabel.
  ///
  /// In en, this message translates to:
  /// **'Password'**
  String get passwordLabel;

  /// No description provided for @signInButton.
  ///
  /// In en, this message translates to:
  /// **'Sign in'**
  String get signInButton;

  /// No description provided for @signUpButton.
  ///
  /// In en, this message translates to:
  /// **'Create account'**
  String get signUpButton;

  /// No description provided for @switchToSignUp.
  ///
  /// In en, this message translates to:
  /// **'No account? Create one'**
  String get switchToSignUp;

  /// No description provided for @switchToSignIn.
  ///
  /// In en, this message translates to:
  /// **'Have an account? Sign in'**
  String get switchToSignIn;

  /// No description provided for @guestButton.
  ///
  /// In en, this message translates to:
  /// **'Continue as guest'**
  String get guestButton;

  /// No description provided for @guestName.
  ///
  /// In en, this message translates to:
  /// **'Guest {code}'**
  String guestName(String code);

  /// No description provided for @nameRequired.
  ///
  /// In en, this message translates to:
  /// **'Enter your name'**
  String get nameRequired;

  /// No description provided for @nameTooLong.
  ///
  /// In en, this message translates to:
  /// **'At most {max} characters'**
  String nameTooLong(int max);

  /// No description provided for @emailInvalid.
  ///
  /// In en, this message translates to:
  /// **'Enter a valid email address'**
  String get emailInvalid;

  /// No description provided for @passwordRequired.
  ///
  /// In en, this message translates to:
  /// **'Enter your password'**
  String get passwordRequired;

  /// No description provided for @errorWrongPassword.
  ///
  /// In en, this message translates to:
  /// **'Wrong email or password.'**
  String get errorWrongPassword;

  /// No description provided for @errorWeakPassword.
  ///
  /// In en, this message translates to:
  /// **'This password is too weak. Use at least 6 characters.'**
  String get errorWeakPassword;

  /// No description provided for @errorEmailInUse.
  ///
  /// In en, this message translates to:
  /// **'An account with this email already exists.'**
  String get errorEmailInUse;

  /// No description provided for @errorInvalidEmail.
  ///
  /// In en, this message translates to:
  /// **'That email address is not valid.'**
  String get errorInvalidEmail;

  /// No description provided for @errorUserNotFound.
  ///
  /// In en, this message translates to:
  /// **'There is no account with this email.'**
  String get errorUserNotFound;

  /// No description provided for @errorTooManyRequests.
  ///
  /// In en, this message translates to:
  /// **'Too many attempts. Try again later.'**
  String get errorTooManyRequests;

  /// No description provided for @errorPermissionDenied.
  ///
  /// In en, this message translates to:
  /// **'You don\'t have permission to do that.'**
  String get errorPermissionDenied;

  /// No description provided for @errorTeamNotFound.
  ///
  /// In en, this message translates to:
  /// **'No team has this code.'**
  String get errorTeamNotFound;

  /// No description provided for @errorFileTooLarge.
  ///
  /// In en, this message translates to:
  /// **'The file is too large (at most {max} MB).'**
  String errorFileTooLarge(int max);

  /// No description provided for @errorNotAnImage.
  ///
  /// In en, this message translates to:
  /// **'Choose an image file.'**
  String get errorNotAnImage;

  /// No description provided for @errorNetwork.
  ///
  /// In en, this message translates to:
  /// **'Can\'t reach the server. Check your connection.'**
  String get errorNetwork;

  /// No description provided for @errorUnknown.
  ///
  /// In en, this message translates to:
  /// **'Something went wrong: {detail}'**
  String errorUnknown(String detail);

  /// No description provided for @offlineBanner.
  ///
  /// In en, this message translates to:
  /// **'You are offline. Changes sync when you reconnect.'**
  String get offlineBanner;

  /// No description provided for @myTeams.
  ///
  /// In en, this message translates to:
  /// **'My teams'**
  String get myTeams;

  /// No description provided for @noTeams.
  ///
  /// In en, this message translates to:
  /// **'No teams yet'**
  String get noTeams;

  /// No description provided for @noTeamsHint.
  ///
  /// In en, this message translates to:
  /// **'Create a team, or join one with the code its owner gives you.'**
  String get noTeamsHint;

  /// No description provided for @createTeam.
  ///
  /// In en, this message translates to:
  /// **'Create team'**
  String get createTeam;

  /// No description provided for @joinTeam.
  ///
  /// In en, this message translates to:
  /// **'Join team'**
  String get joinTeam;

  /// No description provided for @teamNameLabel.
  ///
  /// In en, this message translates to:
  /// **'Team name'**
  String get teamNameLabel;

  /// No description provided for @joinCodeLabel.
  ///
  /// In en, this message translates to:
  /// **'Join code'**
  String get joinCodeLabel;

  /// No description provided for @create.
  ///
  /// In en, this message translates to:
  /// **'Create'**
  String get create;

  /// No description provided for @join.
  ///
  /// In en, this message translates to:
  /// **'Join'**
  String get join;

  /// No description provided for @cancel.
  ///
  /// In en, this message translates to:
  /// **'Cancel'**
  String get cancel;

  /// No description provided for @teamCreated.
  ///
  /// In en, this message translates to:
  /// **'Team created. Its join code is {code}.'**
  String teamCreated(String code);

  /// No description provided for @joinedTeam.
  ///
  /// In en, this message translates to:
  /// **'You joined {name}.'**
  String joinedTeam(String name);

  /// No description provided for @ownerBadge.
  ///
  /// In en, this message translates to:
  /// **'Owner'**
  String get ownerBadge;

  /// No description provided for @profile.
  ///
  /// In en, this message translates to:
  /// **'Profile'**
  String get profile;

  /// No description provided for @tasksTab.
  ///
  /// In en, this message translates to:
  /// **'Tasks'**
  String get tasksTab;

  /// No description provided for @chatTab.
  ///
  /// In en, this message translates to:
  /// **'Chat'**
  String get chatTab;

  /// No description provided for @joinCodeTooltip.
  ///
  /// In en, this message translates to:
  /// **'Join code {code}. Tap to copy.'**
  String joinCodeTooltip(String code);

  /// No description provided for @codeCopied.
  ///
  /// In en, this message translates to:
  /// **'Join code copied'**
  String get codeCopied;

  /// No description provided for @newTaskHint.
  ///
  /// In en, this message translates to:
  /// **'New task'**
  String get newTaskHint;

  /// No description provided for @addTask.
  ///
  /// In en, this message translates to:
  /// **'Add task'**
  String get addTask;

  /// No description provided for @noTasks.
  ///
  /// In en, this message translates to:
  /// **'No tasks yet'**
  String get noTasks;

  /// No description provided for @noTasksHint.
  ///
  /// In en, this message translates to:
  /// **'Add the first one above.'**
  String get noTasksHint;

  /// No description provided for @taskDoneLabel.
  ///
  /// In en, this message translates to:
  /// **'Done: {title}'**
  String taskDoneLabel(String title);

  /// No description provided for @deleteTask.
  ///
  /// In en, this message translates to:
  /// **'Delete'**
  String get deleteTask;

  /// No description provided for @attachFile.
  ///
  /// In en, this message translates to:
  /// **'Attach a file'**
  String get attachFile;

  /// No description provided for @replaceFile.
  ///
  /// In en, this message translates to:
  /// **'Replace the file'**
  String get replaceFile;

  /// No description provided for @openAttachment.
  ///
  /// In en, this message translates to:
  /// **'Open {name}'**
  String openAttachment(String name);

  /// No description provided for @assign.
  ///
  /// In en, this message translates to:
  /// **'Assign'**
  String get assign;

  /// No description provided for @unassigned.
  ///
  /// In en, this message translates to:
  /// **'Nobody'**
  String get unassigned;

  /// No description provided for @assignedTo.
  ///
  /// In en, this message translates to:
  /// **'Assigned to {name}'**
  String assignedTo(String name);

  /// No description provided for @uploading.
  ///
  /// In en, this message translates to:
  /// **'Uploading {percent}%'**
  String uploading(int percent);

  /// No description provided for @taskDeleted.
  ///
  /// In en, this message translates to:
  /// **'Task deleted'**
  String get taskDeleted;

  /// No description provided for @fileAttached.
  ///
  /// In en, this message translates to:
  /// **'File attached'**
  String get fileAttached;

  /// No description provided for @couldNotOpen.
  ///
  /// In en, this message translates to:
  /// **'Could not open the file.'**
  String get couldNotOpen;

  /// No description provided for @openTasks.
  ///
  /// In en, this message translates to:
  /// **'Open'**
  String get openTasks;

  /// No description provided for @doneTasks.
  ///
  /// In en, this message translates to:
  /// **'Done'**
  String get doneTasks;

  /// No description provided for @taskMenu.
  ///
  /// In en, this message translates to:
  /// **'More for {title}'**
  String taskMenu(String title);

  /// No description provided for @messageHint.
  ///
  /// In en, this message translates to:
  /// **'Message'**
  String get messageHint;

  /// No description provided for @send.
  ///
  /// In en, this message translates to:
  /// **'Send'**
  String get send;

  /// No description provided for @noMessages.
  ///
  /// In en, this message translates to:
  /// **'No messages yet'**
  String get noMessages;

  /// No description provided for @noMessagesHint.
  ///
  /// In en, this message translates to:
  /// **'Say hello to your team.'**
  String get noMessagesHint;

  /// No description provided for @onlineNow.
  ///
  /// In en, this message translates to:
  /// **'Online: {names}'**
  String onlineNow(String names);

  /// No description provided for @listSeparator.
  ///
  /// In en, this message translates to:
  /// **', '**
  String get listSeparator;

  /// No description provided for @you.
  ///
  /// In en, this message translates to:
  /// **'you'**
  String get you;

  /// No description provided for @save.
  ///
  /// In en, this message translates to:
  /// **'Save'**
  String get save;

  /// No description provided for @saved.
  ///
  /// In en, this message translates to:
  /// **'Saved'**
  String get saved;

  /// No description provided for @changePicture.
  ///
  /// In en, this message translates to:
  /// **'Change picture'**
  String get changePicture;

  /// No description provided for @removePicture.
  ///
  /// In en, this message translates to:
  /// **'Remove picture'**
  String get removePicture;

  /// No description provided for @language.
  ///
  /// In en, this message translates to:
  /// **'Language'**
  String get language;

  /// No description provided for @languageDevice.
  ///
  /// In en, this message translates to:
  /// **'Device language'**
  String get languageDevice;

  /// No description provided for @languageEnglish.
  ///
  /// In en, this message translates to:
  /// **'English'**
  String get languageEnglish;

  /// No description provided for @languageArabic.
  ///
  /// In en, this message translates to:
  /// **'Arabic'**
  String get languageArabic;

  /// No description provided for @theme.
  ///
  /// In en, this message translates to:
  /// **'Theme'**
  String get theme;

  /// No description provided for @themeSystem.
  ///
  /// In en, this message translates to:
  /// **'System'**
  String get themeSystem;

  /// No description provided for @themeLight.
  ///
  /// In en, this message translates to:
  /// **'Light'**
  String get themeLight;

  /// No description provided for @themeDark.
  ///
  /// In en, this message translates to:
  /// **'Dark'**
  String get themeDark;

  /// No description provided for @signOut.
  ///
  /// In en, this message translates to:
  /// **'Sign out'**
  String get signOut;

  /// No description provided for @signedInAs.
  ///
  /// In en, this message translates to:
  /// **'Signed in as {email}'**
  String signedInAs(String email);

  /// No description provided for @guestAccount.
  ///
  /// In en, this message translates to:
  /// **'Guest account: create an account to keep your data on other devices.'**
  String get guestAccount;

  /// No description provided for @retry.
  ///
  /// In en, this message translates to:
  /// **'Retry'**
  String get retry;

  /// No description provided for @loading.
  ///
  /// In en, this message translates to:
  /// **'Loading'**
  String get loading;

  /// No description provided for @members.
  ///
  /// In en, this message translates to:
  /// **'Members'**
  String get members;
}

class _AppLocalizationsDelegate
    extends LocalizationsDelegate<AppLocalizations> {
  const _AppLocalizationsDelegate();

  @override
  Future<AppLocalizations> load(Locale locale) {
    return SynchronousFuture<AppLocalizations>(lookupAppLocalizations(locale));
  }

  @override
  bool isSupported(Locale locale) =>
      <String>['ar', 'en'].contains(locale.languageCode);

  @override
  bool shouldReload(_AppLocalizationsDelegate old) => false;
}

AppLocalizations lookupAppLocalizations(Locale locale) {
  // Lookup logic when only language code is specified.
  switch (locale.languageCode) {
    case 'ar':
      return AppLocalizationsAr();
    case 'en':
      return AppLocalizationsEn();
  }

  throw FlutterError(
    'AppLocalizations.delegate failed to load unsupported locale "$locale". This is likely '
    'an issue with the localizations generation tool. Please file an issue '
    'on GitHub with a reproducible sample app and the gen-l10n configuration '
    'that was used.',
  );
}
