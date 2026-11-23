// ignore: unused_import
import 'package:intl/intl.dart' as intl;
import 'app_localizations.dart';

// ignore_for_file: type=lint

/// The translations for Arabic (`ar`).
class AppLocalizationsAr extends AppLocalizations {
  AppLocalizationsAr([String locale = 'ar']) : super(locale);

  @override
  String get appTitle => 'لوحة الفريق';

  @override
  String get signInTitle => 'تسجيل الدخول';

  @override
  String get signUpTitle => 'إنشاء حساب';

  @override
  String get nameLabel => 'الاسم';

  @override
  String get emailLabel => 'البريد الإلكتروني';

  @override
  String get passwordLabel => 'كلمة المرور';

  @override
  String get signInButton => 'تسجيل الدخول';

  @override
  String get signUpButton => 'إنشاء الحساب';

  @override
  String get switchToSignUp => 'ليس لديك حساب؟ أنشئ حسابًا';

  @override
  String get switchToSignIn => 'لديك حساب؟ سجّل الدخول';

  @override
  String get guestButton => 'المتابعة كضيف';

  @override
  String guestName(String code) {
    return 'ضيف $code';
  }

  @override
  String get nameRequired => 'أدخل اسمك';

  @override
  String nameTooLong(int max) {
    return '$max حرفًا على الأكثر';
  }

  @override
  String get emailInvalid => 'أدخل بريدًا إلكترونيًا صحيحًا';

  @override
  String get passwordRequired => 'أدخل كلمة المرور';

  @override
  String get errorWrongPassword =>
      'البريد الإلكتروني أو كلمة المرور غير صحيحة.';

  @override
  String get errorWeakPassword =>
      'كلمة المرور ضعيفة جدًا. استخدم ستة أحرف على الأقل.';

  @override
  String get errorEmailInUse => 'يوجد حساب بهذا البريد الإلكتروني.';

  @override
  String get errorInvalidEmail => 'عنوان البريد الإلكتروني غير صالح.';

  @override
  String get errorUserNotFound => 'لا يوجد حساب بهذا البريد الإلكتروني.';

  @override
  String get errorTooManyRequests => 'محاولات كثيرة جدًا. حاول لاحقًا.';

  @override
  String get errorPermissionDenied => 'ليست لديك صلاحية للقيام بذلك.';

  @override
  String get errorTeamNotFound => 'لا يوجد فريق بهذا الرمز.';

  @override
  String errorFileTooLarge(int max) {
    return 'الملف كبير جدًا (الحد الأقصى $max ميغابايت).';
  }

  @override
  String get errorNotAnImage => 'اختر ملف صورة.';

  @override
  String get errorNetwork => 'تعذّر الوصول إلى الخادم. تحقّق من اتصالك.';

  @override
  String errorUnknown(String detail) {
    return 'حدث خطأ: $detail';
  }

  @override
  String get offlineBanner =>
      'أنت غير متصل. تُزامَن التغييرات عند عودة الاتصال.';

  @override
  String get myTeams => 'فرقي';

  @override
  String get noTeams => 'لا توجد فرق بعد';

  @override
  String get noTeamsHint =>
      'أنشئ فريقًا، أو انضم إلى فريق بالرمز الذي يعطيك إياه مالكه.';

  @override
  String get createTeam => 'إنشاء فريق';

  @override
  String get joinTeam => 'الانضمام إلى فريق';

  @override
  String get teamNameLabel => 'اسم الفريق';

  @override
  String get joinCodeLabel => 'رمز الانضمام';

  @override
  String get create => 'إنشاء';

  @override
  String get join => 'انضمام';

  @override
  String get cancel => 'إلغاء';

  @override
  String teamCreated(String code) {
    return 'أُنشئ الفريق. رمز الانضمام: $code.';
  }

  @override
  String joinedTeam(String name) {
    return 'انضممت إلى $name.';
  }

  @override
  String get ownerBadge => 'المالك';

  @override
  String get profile => 'الملف الشخصي';

  @override
  String get tasksTab => 'المهام';

  @override
  String get chatTab => 'الدردشة';

  @override
  String joinCodeTooltip(String code) {
    return 'رمز الانضمام $code. اضغط للنسخ.';
  }

  @override
  String get codeCopied => 'نُسخ رمز الانضمام';

  @override
  String get newTaskHint => 'مهمة جديدة';

  @override
  String get addTask => 'إضافة مهمة';

  @override
  String get noTasks => 'لا توجد مهام بعد';

  @override
  String get noTasksHint => 'أضف أول مهمة في الأعلى.';

  @override
  String taskDoneLabel(String title) {
    return 'منجزة: $title';
  }

  @override
  String get deleteTask => 'حذف';

  @override
  String get attachFile => 'إرفاق ملف';

  @override
  String get replaceFile => 'استبدال الملف';

  @override
  String openAttachment(String name) {
    return 'فتح $name';
  }

  @override
  String get assign => 'إسناد';

  @override
  String get unassigned => 'لا أحد';

  @override
  String assignedTo(String name) {
    return 'مُسندة إلى $name';
  }

  @override
  String uploading(int percent) {
    return 'جارٍ الرفع $percent٪';
  }

  @override
  String get taskDeleted => 'حُذفت المهمة';

  @override
  String get fileAttached => 'أُرفق الملف';

  @override
  String get couldNotOpen => 'تعذّر فتح الملف.';

  @override
  String get openTasks => 'مفتوحة';

  @override
  String get doneTasks => 'منجزة';

  @override
  String taskMenu(String title) {
    return 'المزيد عن $title';
  }

  @override
  String get messageHint => 'رسالة';

  @override
  String get send => 'إرسال';

  @override
  String get noMessages => 'لا توجد رسائل بعد';

  @override
  String get noMessagesHint => 'ألقِ التحية على فريقك.';

  @override
  String onlineNow(String names) {
    return 'متصل الآن: $names';
  }

  @override
  String get listSeparator => '، ';

  @override
  String get you => 'أنت';

  @override
  String get save => 'حفظ';

  @override
  String get saved => 'تم الحفظ';

  @override
  String get changePicture => 'تغيير الصورة';

  @override
  String get removePicture => 'إزالة الصورة';

  @override
  String get language => 'اللغة';

  @override
  String get languageDevice => 'لغة الجهاز';

  @override
  String get languageEnglish => 'الإنجليزية';

  @override
  String get languageArabic => 'العربية';

  @override
  String get theme => 'المظهر';

  @override
  String get themeSystem => 'حسب الجهاز';

  @override
  String get themeLight => 'فاتح';

  @override
  String get themeDark => 'داكن';

  @override
  String get signOut => 'تسجيل الخروج';

  @override
  String signedInAs(String email) {
    return 'مسجّل الدخول بالبريد $email';
  }

  @override
  String get guestAccount =>
      'حساب ضيف: أنشئ حسابًا لتصل إلى بياناتك من أجهزة أخرى.';

  @override
  String get retry => 'إعادة المحاولة';

  @override
  String get loading => 'جارٍ التحميل';

  @override
  String get members => 'الأعضاء';
}
