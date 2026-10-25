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

        let selectAll = allowEverything(app)
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

    /// What App Review does (docs/appstore/review-notes.md): the demo server's address and
    /// setup code typed in by hand. HKR_DEMO_SERVER and HKR_DEMO_CODE name the server; the
    /// screenshots of this run are the App Store set (.github/workflows/appstore-screenshots.yml).
    func testManualSetupWithTheDemoServer() throws {
        let env = ProcessInfo.processInfo.environment
        guard let server = env["HKR_DEMO_SERVER"], let code = env["HKR_DEMO_CODE"], !server.isEmpty, !code.isEmpty else {
            throw XCTSkip("needs HKR_DEMO_SERVER and HKR_DEMO_CODE")
        }
        let app = XCUIApplication()
        app.launchArguments = ["-hkr-reset-state"]
        app.launch()
        XCTAssertTrue(app.staticTexts["Health Relay"].waitForExistence(timeout: 15))
        sleep(1)
        snapshot("01-welcome")

        app.buttons["manual"].tap()
        let field = app.textFields["manual.server"]
        XCTAssertTrue(field.waitForExistence(timeout: 10))
        field.tap()
        field.typeText(server)
        // The screenshot shows the example code from the form's footer, never the demo's own:
        // App Store screenshots are public.
        let codeField = app.textFields["manual.code"]
        codeField.tap()
        codeField.typeText("ABCD-EFGH-JKLM-NPQR")
        app.navigationBars["Server Details"].tap() // keyboard away
        sleep(1)
        snapshot("02-server-details")
        codeField.tap()
        codeField.typeText(String(repeating: XCUIKeyboardKey.delete.rawValue, count: 19) + code)
        app.buttons["manual.connect"].tap()

        let primer = app.buttons["health.continue"]
        XCTAssertTrue(primer.waitForExistence(timeout: 60), "no Health step:\n\(app.debugDescription)")
        sleep(1)
        snapshot("03-health-step")
        primer.tap()
        _ = allowEverything(app, shots: false)

        let title = app.staticTexts["status.title"]
        expectation(for: NSPredicate(format: "label == 'Up to date'"), evaluatedWith: title)
        waitForExpectations(timeout: 300)
        sleep(3)
        snapshot("04-home")

        app.buttons["advanced"].tap()
        XCTAssertTrue(app.navigationBars["Advanced"].waitForExistence(timeout: 10))
        sleep(3)
        snapshot("05-advanced")
        let types = app.buttons.matching(NSPredicate(format: "label BEGINSWITH 'Data types read'")).firstMatch
        if types.waitForExistence(timeout: 10) {
            types.tap()
            sleep(2)
            snapshot("06-data-types")
        }
    }

    /// Health's sheet: select everything ("Turn On All" before iOS 26), confirm at the end of
    /// the list ("Continue" on iOS 27, "Allow" before), then, on iOS 27, all history. Returns
    /// the select-all control, gone once the sheet is.
    @discardableResult
    private func allowEverything(_ app: XCUIApplication, shots: Bool = true) -> XCUIElement {
        let selectAll = app.descendants(matching: .any).matching(NSPredicate(
            format: "identifier == 'UIA.Health.AuthSheet.AllCategoryButton' OR label == 'Turn On All'")).firstMatch
        XCTAssertTrue(selectAll.waitForExistence(timeout: 30), "no Health sheet:\n\(app.debugDescription)")
        sleep(1)
        if shots { snapshot("4-health-sheet") }
        selectAll.tap()
        // Resolved anew on each use: labels change as the sheet moves on.
        let confirm = app.descendants(matching: .any).matching(NSPredicate(
            format: "label IN {'Continue', 'Allow'} AND identifier != 'health.continue'")).firstMatch
        let allHistory = app.descendants(matching: .any).matching(NSPredicate(format: "label BEGINSWITH 'All Recorded Data'")).firstMatch
        for _ in 0..<6 {
            if allHistory.exists {
                if shots { snapshot("5-health-history") }
                allHistory.tap()
                sleep(1)
            }
            guard confirm.waitForExistence(timeout: 5) else { break }
            var swipes = 0
            // On screen by its frame: isHittable can raise for a button far down Health's list
            // ("Activation point invalid").
            let screen = app.windows.firstMatch.frame
            while swipes < 60 && confirm.exists && !(screen.contains(CGPoint(x: confirm.frame.midX, y: confirm.frame.midY))) {
                app.swipeUp(velocity: .fast)
                swipes += 1
            }
            if confirm.exists && confirm.isEnabled { confirm.tap() }
            sleep(2)
        }
        XCTAssertFalse(selectAll.exists || allHistory.exists, "Health sheet still up:\n\(app.debugDescription)")
        return selectAll
    }

    private func snapshot(_ name: String) {
        let a = XCTAttachment(screenshot: XCUIScreen.main.screenshot())
        a.name = name
        a.lifetime = .keepAlways
        add(a)
    }
}
