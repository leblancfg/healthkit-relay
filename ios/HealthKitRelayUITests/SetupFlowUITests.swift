import XCTest

/// The whole setup as a user goes through it: the server's setup page in Safari, the link
/// into the app, the one Health sheet, the first sync. Screenshots are kept in the result
/// bundle (Scripts/ui-test exports them).
final class SetupFlowUITests: XCTestCase {
    override func setUp() {
        continueAfterFailure = false
    }

    func testSetupFromTheServerPage() throws {
        guard let page = ProcessInfo.processInfo.environment["HKR_SETUP_URL"], let setupURL = URL(string: page) else {
            throw XCTSkip("needs a running server: see Scripts/ui-test")
        }
        let app = XCUIApplication()
        app.launchArguments = ["-hkr-reset-state"]
        app.launch()
        XCTAssertTrue(app.staticTexts["Health Relay"].waitForExistence(timeout: 15))
        snapshot("1-welcome")

        // As when the setup link is tapped in a message: Safari shows the setup page.
        XCUIDevice.shared.system.open(setupURL)
        let safari = XCUIApplication(bundleIdentifier: "com.apple.mobilesafari")
        XCTAssertTrue(safari.wait(for: .runningForeground, timeout: 30))
        let open = safari.links["Open in Health Relay"]
        XCTAssertTrue(open.waitForExistence(timeout: 30), "setup page not shown:\n\(safari.debugDescription)")
        sleep(1)
        snapshot("2-setup-page")
        open.tap()
        let openApp = safari.buttons["Open"]
        if openApp.waitForExistence(timeout: 5) { openApp.tap() }
        XCTAssertTrue(app.wait(for: .runningForeground, timeout: 20))

        // A link for a new server asks first: it names where Health data will go.
        let connect = app.alerts.buttons["Connect"]
        XCTAssertTrue(connect.waitForExistence(timeout: 20), "no confirmation:\n\(app.debugDescription)")
        snapshot("2b-confirm-server")
        connect.tap()

        // The app says what to pick, then Health's sheet comes on Continue.
        let primer = app.buttons["health.continue"]
        XCTAssertTrue(primer.waitForExistence(timeout: 20), "no Health step:\n\(app.debugDescription)")
        sleep(1)
        snapshot("3-health-step")
        primer.tap()

        // Health's sheet: select everything ("Turn On All" before iOS 26), confirm at the end
        // of the list ("Continue" on iOS 27, "Allow" before), then, on iOS 27, all history.
        let selectAll = app.descendants(matching: .any).matching(NSPredicate(
            format: "identifier == 'UIA.Health.AuthSheet.AllCategoryButton' OR label == 'Turn On All'")).firstMatch
        XCTAssertTrue(selectAll.waitForExistence(timeout: 30), "no Health sheet:\n\(app.debugDescription)")
        sleep(1)
        snapshot("4-health-sheet")
        selectAll.tap()
        // Resolved anew on each use: labels change as the sheet moves on.
        let confirm = app.descendants(matching: .any).matching(NSPredicate(
            format: "label IN {'Continue', 'Allow'} AND identifier != 'health.continue'")).firstMatch
        let allHistory = app.descendants(matching: .any).matching(NSPredicate(format: "label BEGINSWITH 'All Recorded Data'")).firstMatch
        for _ in 0..<6 {
            if allHistory.exists {
                snapshot("5-health-history")
                allHistory.tap()
                sleep(1)
            }
            guard confirm.waitForExistence(timeout: 5) else { break }
            var swipes = 0
            while swipes < 60 && confirm.exists && !confirm.isHittable {
                app.swipeUp(velocity: .fast)
                swipes += 1
            }
            if confirm.exists && confirm.isEnabled { confirm.tap() }
            sleep(2)
        }
        XCTAssertFalse(selectAll.exists || allHistory.exists, "Health sheet still up:\n\(app.debugDescription)")

        let title = app.staticTexts["status.title"]
        expectation(for: NSPredicate(format: "label == 'Up to date'"), evaluatedWith: title)
        waitForExpectations(timeout: 240)
        sleep(2)
        snapshot("6-home")

        // Opening the app again does not ask anything.
        XCUIDevice.shared.press(.home)
        app.activate()
        XCTAssertTrue(title.waitForExistence(timeout: 10))
        XCTAssertFalse(selectAll.waitForExistence(timeout: 3))
        XCTAssertFalse(primer.exists)

        app.buttons["advanced"].tap()
        XCTAssertTrue(app.navigationBars["Advanced"].waitForExistence(timeout: 10))
        sleep(2)
        snapshot("7-advanced")

        // What Health shows for an item-by-item type with nothing in Health to pick.
        let vision = app.buttons.matching(NSPredicate(format: "label BEGINSWITH 'Vision prescriptions'")).firstMatch
        if vision.waitForExistence(timeout: 5) {
            vision.tap()
            sleep(6)
            snapshot("8-vision-prescriptions")
        }
    }

    private func snapshot(_ name: String) {
        let a = XCTAttachment(screenshot: XCUIScreen.main.screenshot())
        a.name = name
        a.lifetime = .keepAlways
        add(a)
    }
}
