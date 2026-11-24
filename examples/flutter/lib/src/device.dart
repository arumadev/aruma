import 'dart:typed_data';

import 'package:file_picker/file_picker.dart';
import 'package:url_launcher/url_launcher.dart';

/// A file the user picked.
class PickedFile {
  const PickedFile({required this.name, required this.bytes, required this.contentType});

  final String name;
  final Uint8List bytes;
  final String contentType;
}

/// What the app asks of the device: a file, and opening a link. The widget tests replace it.
class Device {
  const Device();

  /// Null when the user cancelled.
  Future<PickedFile?> pickFile({bool images = false}) async {
    final f = await FilePicker.pickFile(type: images ? FileType.image : FileType.any);
    if (f == null) return null;
    return PickedFile(name: f.name, bytes: await f.readAsBytes(), contentType: contentTypeOf(f.name));
  }

  /// Opens the link in the browser (a new tab on the web) or the app that shows the file.
  Future<bool> open(Uri url) => launchUrl(url, mode: LaunchMode.externalApplication);
}

const _types = {
  'png': 'image/png',
  'jpg': 'image/jpeg',
  'jpeg': 'image/jpeg',
  'gif': 'image/gif',
  'webp': 'image/webp',
  'heic': 'image/heic',
  'pdf': 'application/pdf',
  'txt': 'text/plain',
  'md': 'text/markdown',
  'csv': 'text/csv',
  'json': 'application/json',
  'zip': 'application/zip',
};

/// The content type for a file name, by its extension.
String contentTypeOf(String name) {
  final dot = name.lastIndexOf('.');
  final ext = dot < 0 ? '' : name.substring(dot + 1).toLowerCase();
  return _types[ext] ?? 'application/octet-stream';
}
