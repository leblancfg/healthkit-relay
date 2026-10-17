import HealthKit

/// One HealthKit data type to sync.
struct SyncedType: Sendable {
    let type: HKSampleType
    /// Unit values are converted to before upload (quantity types only). Fixed per type, so
    /// the server sees the same unit whatever the phone's region settings.
    let unit: HKUnit?

    static func quantity(_ id: HKQuantityTypeIdentifier, _ unit: HKUnit) -> SyncedType {
        SyncedType(type: HKQuantityType(id), unit: unit)
    }

    static func category(_ id: HKCategoryTypeIdentifier) -> SyncedType {
        SyncedType(type: HKCategoryType(id), unit: nil)
    }

    static let workouts = SyncedType(type: HKWorkoutType.workoutType(), unit: nil)

    /// Stable key for persisted state (anchors).
    var key: String { type.identifier }
}

private let bpm = HKUnit.count().unitDivided(by: .minute())

/// Everything the app reads and uploads. Adding a HealthKit type is one line here.
enum HealthTypes {
    static let all: [SyncedType] = [
        // Heart and lungs
        .quantity(.heartRate, bpm),
        .quantity(.restingHeartRate, bpm),
        .quantity(.walkingHeartRateAverage, bpm),
        .quantity(.heartRateVariabilitySDNN, .secondUnit(with: .milli)),
        .quantity(.heartRateRecoveryOneMinute, bpm),
        .quantity(.respiratoryRate, bpm),
        .quantity(.oxygenSaturation, .percent()),
        .quantity(.vo2Max, HKUnit.literUnit(with: .milli).unitDivided(by: HKUnit.gramUnit(with: .kilo).unitMultiplied(by: .minute()))),
        .quantity(.appleSleepingWristTemperature, .degreeCelsius()),
        // Activity
        .quantity(.stepCount, .count()),
        .quantity(.distanceWalkingRunning, .meter()),
        .quantity(.distanceCycling, .meter()),
        .quantity(.flightsClimbed, .count()),
        .quantity(.activeEnergyBurned, .kilocalorie()),
        .quantity(.basalEnergyBurned, .kilocalorie()),
        .quantity(.appleExerciseTime, .minute()),
        .quantity(.appleStandTime, .minute()),
        .quantity(.walkingSpeed, HKUnit.meter().unitDivided(by: .second())),
        // Body
        .quantity(.bodyMass, .gramUnit(with: .kilo)),
        .quantity(.bodyFatPercentage, .percent()),
        // Environment
        .quantity(.environmentalAudioExposure, .decibelAWeightedSoundPressureLevel()),
        .quantity(.headphoneAudioExposure, .decibelAWeightedSoundPressureLevel()),
        // Categories
        .category(.sleepAnalysis),
        .category(.mindfulSession),
        .category(.appleStandHour),
        .category(.highHeartRateEvent),
        .category(.lowHeartRateEvent),
        .category(.irregularHeartRhythmEvent),
        // Workouts
        .workouts,
    ]

    static var readTypes: Set<HKObjectType> { Set(all.map(\.type)) }
}
