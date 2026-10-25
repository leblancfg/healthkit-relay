import Foundation

/// The app's data directories, Application Support/<name>: readable after the first unlock
/// (background syncs read them while the iPhone is locked) and left out of iCloud and computer
/// backups. Upload bodies hold Health data, which belongs only in Health and on the user's
/// server; sync positions describe this iPhone's Health database and mean nothing on another.
enum AppDirectory {
    static func make(_ name: String) -> URL {
        let base = FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask)[0]
        var url = base.appendingPathComponent(name, isDirectory: true)
        try? FileManager.default.createDirectory(
            at: url, withIntermediateDirectories: true,
            attributes: [.protectionKey: FileProtectionType.completeUntilFirstUserAuthentication])
        var values = URLResourceValues()
        values.isExcludedFromBackup = true
        try? url.setResourceValues(values)
        return url
    }
}
