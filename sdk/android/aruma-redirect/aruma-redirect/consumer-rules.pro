# The provider is instantiated by name from the merged manifest.
-keep class dev.aruma.redirect.ArumaRedirectProvider { <init>(); }
-keep class dev.aruma.redirect.ArumaRedirect { public *; }
# Keeps the configuration error readable in a shrunk app's crash log.
-keep class dev.aruma.redirect.ArumaRedirectException
# The launch check reads firebase-storage's base by reflection (StorageGuard); nothing when firebase-storage is absent.
-keep class com.google.firebase.storage.network.NetworkRequest { public static android.net.Uri PROD_BASE_URL; }
