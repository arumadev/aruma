# Copyright 2026 The Aruma Authors
# SPDX-License-Identifier: Apache-2.0
#
# ruby sdk/ios/ArumaRedirect/Scripts/aruma_storage_test.rb
#
# Applies storage-host.patch to a pod tree built from the patch's own old lines (no CocoaPods and
# no network). test/e2e/ios-storage/cocoapods.sh runs it on a real `pod install`.

require 'fileutils'
require 'minitest/autorun'
require 'tmpdir'
require_relative 'aruma_storage'

class ArumaStorageTest < Minitest::Test
  PATCH = ArumaStorage::DEFAULT_PATCH
  FILLER = "\n// unrelated official code\n\n"

  def setup
    @root = Dir.mktmpdir('aruma-storage-test')
    @files = ArumaStorage.parse(File.read(PATCH)).select { |f| f.path.start_with?(ArumaStorage::SOURCES) }
    @files.each do |f|
      FileUtils.mkdir_p(File.join(@root, File.dirname(f.path)))
      next if f.created

      File.write(File.join(@root, f.path), f.hunks.map(&:first).join(FILLER))
    end
  end

  def teardown
    FileUtils.rm_rf(@root)
  end

  def read(path)
    File.read(File.join(@root, path))
  end

  def test_the_patch_touches_the_four_places
    storage = @files.find { |f| f.path.end_with?('/Storage.swift') }
    assert storage, 'Storage.swift is patched'
    olds = storage.hunks.map(&:first).join
    assert_includes olds, "    self.port = port\n"               # useEmulator (Storage.swift:225)
    assert_includes olds, "    port = 443\n"                     # init (Storage.swift:287)
    assert_includes olds, "  var port: Int\n"
    %w[StorageGetDownloadURLTask.swift StorageUtils.swift].each do |name|
      f = @files.find { |x| x.path.end_with?("/#{name}") }
      assert_includes f.hunks.map(&:first).join, "    components.port = reference.storage.port\n", name
    end
    assert(@files.any? { |f| f.created && f.path.end_with?('ArumaStorageOrigin.swift') })
    refute(@files.any? { |f| f.path.include?('/Tests/') }, 'tests are not part of the pod')
  end

  def test_applies_once_and_again_changes_nothing
    created = ArumaStorage.apply(@root, PATCH)
    assert_equal ['FirebaseStorage/Sources/Internal/ArumaStorageOrigin.swift'], created
    assert_includes read('FirebaseStorage/Sources/Storage.swift'), "    let origin = ArumaStorageOrigin.main\n"
    assert_includes read('FirebaseStorage/Sources/Storage.swift'), "  var port: Int?\n"
    before = @files.to_h { |f| [f.path, read(f.path)] }
    ArumaStorage.apply(@root, PATCH)
    @files.each { |f| assert_equal before[f.path], read(f.path), f.path }
  end

  # A release that moved a line fails pod install, and leaves every file as it was.
  def test_a_missing_line_fails_and_writes_nothing
    path = 'FirebaseStorage/Sources/Storage.swift'
    File.write(File.join(@root, path), read(path).sub("    port = 443\n", "    port = 8443\n"))
    before = @files.reject(&:created).to_h { |f| [f.path, read(f.path)] }
    err = assert_raises(ArumaStorage::PatchError) { ArumaStorage.apply(@root, PATCH) }
    assert_match(/Storage\.swift has 0 copies of these lines/, err.message)
    before.each { |p, text| assert_equal text, read(p), p }
    refute File.exist?(File.join(@root, 'FirebaseStorage/Sources/Internal/ArumaStorageOrigin.swift'))
  end

  def test_two_copies_fail
    path = 'FirebaseStorage/Sources/Internal/StorageUtils.swift'
    File.write(File.join(@root, path), read(path) * 2)
    err = assert_raises(ArumaStorage::PatchError) { ArumaStorage.apply(@root, PATCH) }
    assert_match(/StorageUtils\.swift has 2 copies/, err.message)
  end

  def test_a_missing_file_fails
    FileUtils.rm(File.join(@root, 'FirebaseStorage/Sources/Internal/StorageFetcherService.swift'))
    err = assert_raises(ArumaStorage::PatchError) { ArumaStorage.apply(@root, PATCH) }
    assert_match(/StorageFetcherService\.swift is not in the pod/, err.message)
  end
end
