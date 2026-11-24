{{flutter_js}}
{{flutter_build_config}}

// The app bundles the fonts it draws with (NotoSans and NotoSansArabic, pubspec.yaml), so the
// engine's fallback fonts are not needed: it looks for them on this origin instead of
// fonts.gstatic.com, and the page asks Google for no font.
_flutter.loader.load({ config: { fontFallbackBaseUrl: 'assets/fonts/fallback/' } });
