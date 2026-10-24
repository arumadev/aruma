# Copyright 2026 The Aruma Authors
# SPDX-License-Identifier: Apache-2.0
#
# CocoaPods helper: patches the FirebaseStorage pod in Pods/ with the same patch the SwiftPM mirror
# is built from (sdk/ios/firebase-storage-patch/storage-host.patch, ADR-022 §16.3), so that
# FirebaseStorage reads ArumaURL from Info.plist and returns download URLs on Aruma.
#
# Only for the FirebaseStorage versions still published to CocoaPods (Firebase stopped publishing
# there in October 2026); SwiftPM with the mirror is the main path. In the Podfile:
#
#   post_install do |installer|
#     require_relative '<aruma>/sdk/ios/ArumaRedirect/Scripts/aruma_storage'
#     ArumaStorage.patch!(installer)
#   end
#
# Every hunk must match the pod's sources exactly once, or `pod install` fails with the file and
# the missing lines: a FirebaseStorage release that moved them is never half patched or silently
# left unpatched. Running `pod install` again on an already patched pod changes nothing.

module ArumaStorage
  DEFAULT_PATCH = File.expand_path('../../firebase-storage-patch/storage-host.patch', __dir__)
  SOURCES = 'FirebaseStorage/Sources/'.freeze

  class PatchError < StandardError; end

  # One file of a unified diff: its path, whether it is new, and its hunks as [old, new] texts.
  FilePatch = Struct.new(:path, :created, :hunks)

  module_function

  # Patches Pods/FirebaseStorage and, after the projects are generated (post_install), adds the new
  # source files to every FirebaseStorage pod target. Returns the patched FirebaseStorage version.
  #
  # All or nothing: every hunk and every Xcode target is resolved before any file is written.
  def patch!(installer, patch: ENV['ARUMA_STORAGE_PATCH'] || DEFAULT_PATCH)
    pod_targets = installer.pod_targets.select { |t| t.pod_name == 'FirebaseStorage' }
    raise PatchError, 'ArumaStorage: the Podfile has no FirebaseStorage pod' if pod_targets.empty?

    version = pod_targets.first.root_spec.version.to_s
    pod_dir = installer.sandbox.pod_dir('FirebaseStorage').to_s
    planned = begin
      plan_all(pod_dir, patch)
    rescue PatchError => e
      raise PatchError, "#{e.message}\n(FirebaseStorage #{version}; the helper supports the releases " \
                        'still published to CocoaPods, and SwiftPM with the mirror is the main path)'
    end
    native = native_targets(installer, pod_targets)
    write(pod_dir, planned)
    created = planned.map(&:first).select(&:created).map(&:path)
    native.each { |t| add_to_target(t, pod_dir, created) }
    Pod::UI.puts "ArumaStorage: FirebaseStorage #{version} patched with #{File.basename(patch)} " \
                 "(it reads ArumaURL from Info.plist), in #{native.map(&:name).join(', ')}"
    version
  end

  # Applies the patch's FirebaseStorage/Sources files under root. Returns the paths it created.
  def apply(root, patch_path)
    planned = plan_all(root, patch_path)
    write(root, planned)
    planned.map(&:first).select(&:created).map(&:path)
  end

  # [[FilePatch, new text or nil when already patched]] for every file, or a PatchError.
  def plan_all(root, patch_path)
    files = parse(File.read(patch_path)).select { |f| f.path.start_with?(SOURCES) }
    raise PatchError, "ArumaStorage: #{patch_path} changes no file under #{SOURCES}" if files.empty?

    files.map { |f| [f, plan(root, f, patch_path)] }
  end

  # The Xcode targets of the FirebaseStorage pod targets (by label, which differs from the pod's
  # name when it is built for several platforms), in the Pods project and in the generated
  # projects of `generate_multiple_pod_projects`. Before the projects exist (pre_install), none.
  def native_targets(installer, pod_targets)
    projects = [installer.respond_to?(:pods_project) ? installer.pods_project : nil]
    projects += Array(installer.generated_projects) if installer.respond_to?(:generated_projects)
    projects = projects.compact.uniq
    return [] if projects.empty?

    pod_targets.map(&:label).uniq.map do |label|
      found = projects.flat_map(&:targets).find { |t| t.name == label }
      raise PatchError, "ArumaStorage: no Xcode target #{label} for the FirebaseStorage pod" if found.nil?

      found
    end
  end

  def write(root, planned)
    planned.each do |f, text|
      next if text.nil?

      dest = File.join(root, f.path)
      File.chmod(File.stat(dest).mode | 0o200, dest) if File.exist?(dest)
      File.write(dest, text)
    end
  end

  # The new text of one file, or nil when it is already patched.
  def plan(root, file, patch_path)
    dest = File.join(root, file.path)
    if file.created
      text = file.hunks.map(&:last).join
      return nil if File.exist?(dest) && File.read(dest) == text
      raise PatchError, "ArumaStorage: #{file.path} already exists in the pod and differs from #{patch_path}" if File.exist?(dest)
      raise PatchError, "ArumaStorage: #{File.dirname(file.path)} is not in the pod" unless File.directory?(File.dirname(dest))

      return text
    end
    raise PatchError, "ArumaStorage: #{file.path} is not in the pod" unless File.exist?(dest)

    text = File.read(dest)
    patched = file.hunks.all? { |_, new| text.scan(new).length == 1 } &&
              file.hunks.none? { |old, _| text.include?(old) }
    return nil if patched

    file.hunks.each do |old, new|
      count = text.scan(old).length
      next text = text.sub(old) { new } if count == 1

      raise PatchError, "ArumaStorage: #{file.path} has #{count} copies of these lines, not one, so " \
                        "this FirebaseStorage release cannot be patched by #{patch_path}:\n#{old}"
    end
    text
  end

  # Unified diff → FilePatch list. Only what storage-host.patch uses: ---/+++ headers and @@ hunks.
  def parse(diff)
    files = []
    lines = diff.lines
    i = 0
    while i < lines.length
      line = lines[i]
      i += 1
      next unless line.start_with?('--- ')

      created = line.chomp == '--- /dev/null'
      target = lines[i]
      raise PatchError, "ArumaStorage: no +++ line after #{line}" unless target&.start_with?('+++ b/')

      i += 1
      file = FilePatch.new(target.chomp.delete_prefix('+++ b/'), created, [])
      while i < lines.length && lines[i].start_with?('@@')
        i += 1
        old = +''
        new = +''
        while i < lines.length && !lines[i].start_with?('@@', '--- ')
          l = lines[i]
          i += 1
          case l[0]
          when ' ' then old << l[1..]; new << l[1..]
          when '-' then old << l[1..]
          when '+' then new << l[1..]
          when '\\' then next
          else old << "\n"; new << "\n" # an empty context line
          end
        end
        file.hunks << [old, new]
      end
      files << file
    end
    files
  end

  # post_install: the project was generated before the new files existed.
  def add_to_target(target, pod_dir, paths)
    project = target.project
    paths.each do |path|
      full = File.join(pod_dir, path)
      next if target.source_build_phase.files_references.any? { |r| r.real_path.to_s == full }

      group = (project.respond_to?(:pod_group) && project.pod_group('FirebaseStorage')) || project.main_group
      target.add_file_references([group.new_file(full)])
    end
  end
end
