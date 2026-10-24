{{flutter_js}}
{{flutter_build_config}}

// The e2e page has no network beyond this machine: the engine's default font (Roboto) and its
// fallback fonts come from this origin, where there are none, instead of fonts.gstatic.com.
_flutter.loader.load({ config: { fontFallbackBaseUrl: 'fonts-not-here/' } });
