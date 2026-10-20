# The provider is instantiated by name from the merged manifest.
-keep class dev.aruma.redirect.ArumaRedirectProvider { <init>(); }
-keep class dev.aruma.redirect.ArumaRedirect { public *; }
# Keeps the configuration error readable in a shrunk app's crash log.
-keep class dev.aruma.redirect.ArumaRedirectException
