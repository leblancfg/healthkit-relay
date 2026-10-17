import CoreLocation
import Foundation
import HealthKit
import UniformTypeIdentifiers

/// Turns any HealthKit sample into its wire JSON, plus the records that carry its bulk data
/// (route points, heartbeats, ECG voltages, quantity series, documents and attachments), split
/// into chunks so no single upload grows too large. Sample kinds this app does not know are
/// still uploaded, with their full NSKeyedArchiver archive.
struct SampleEncoder: Sendable {
    let store: HKHealthStore
    let units: UnitResolver

    /// Points per series record and bytes per file chunk: each record stays well under 1 MB.
    static let routePointsPerChunk = 4000
    static let heartbeatsPerChunk = 20000
    static let voltagesPerChunk = 16000
    static let seriesPointsPerChunk = 8000
    static let blobBytesPerChunk = 512 * 1024

    struct Output {
        var sample: JSON
        var records: [JSON] = []
    }

    func encode(_ s: HKSample) async -> Output {
        var o = common(s)
        var records: [JSON] = []

        switch s {
        case let q as HKQuantitySample:
            o["kind"] = "quantity"
            await quantity(q, into: &o, records: &records)
        case let c as HKCategorySample:
            o["kind"] = "category"
            category(c, into: &o)
        case let w as HKWorkout:
            o["kind"] = "workout"
            await workout(w, into: &o)
        case let c as HKCorrelation:
            o["kind"] = "correlation"
            var members: [JSON] = []
            for m in c.objects.sorted(by: { $0.uuid.uuidString < $1.uuid.uuidString }) {
                let enc = await encode(m)
                members.append(enc.sample)
                records += enc.records
            }
            o["objects"] = .array(members)
        case let r as HKWorkoutRoute:
            o["kind"] = "workout_route"
            o["count"] = JSON(r.count)
            records += await route(r)
        case let h as HKHeartbeatSeriesSample:
            o["kind"] = "heartbeat_series"
            o["count"] = JSON(h.count)
            records += await heartbeats(h)
        case let e as HKElectrocardiogram:
            o["kind"] = "electrocardiogram"
            records += await electrocardiogram(e, into: &o)
        case let a as HKAudiogramSample:
            o["kind"] = "audiogram"
            o["sensitivity_points"] = .array(a.sensitivityPoints.map(audiogramPoint))
        case let r as HKClinicalRecord:
            o["kind"] = "clinical_record"
            clinical(r, into: &o)
        case let d as HKCDADocumentSample:
            o["kind"] = "cda_document"
            records += await cda(d, into: &o)
        case let v as HKVisionPrescription:
            o["kind"] = "vision_prescription"
            vision(v, into: &o)
            records += await attachments(for: v)
        default:
            o["kind"] = "sample"
            other(s, into: &o)
        }
        // The complete object for every kind but the two bulk ones, so nothing HealthKit
        // stores is lost even where this encoder has no field for it.
        if !(s is HKQuantitySample || s is HKCategorySample) {
            o.put("archive", JSON.archive(s))
        }
        return Output(sample: .object(o), records: records)
    }

    // MARK: Common

    private func common(_ s: HKSample) -> [String: JSON] {
        var o: [String: JSON] = [
            "uuid": .string(s.uuid.uuidString),
            "type": .string(s.sampleType.identifier),
            "start": .string(WireEncoder.timestamp(s.startDate)),
            "end": .string(WireEncoder.timestamp(s.endDate)),
            "source": WireEncoder.source(s),
        ]
        o.put("device", WireEncoder.device(s.device))
        o.put("metadata", JSON.metadata(s.metadata))
        if s.hasUndeterminedDuration { o["undetermined_duration"] = true }
        return o
    }

    private func q(_ quantity: HKQuantity?, _ unit: HKUnit? = nil) -> JSON {
        QuantityCodec.encode(quantity, preferred: unit)
    }

    // MARK: Quantities and categories

    private func quantity(_ s: HKQuantitySample, into o: inout [String: JSON], records: inout [JSON]) async {
        let unit = await units.unit(for: s.quantityType)
        if let split = QuantityCodec.split(s.quantity, preferred: unit) {
            o["value"] = .double(split.value)
            o["unit"] = .string(split.unit)
        } else {
            o["quantity_text"] = .string(String(describing: s.quantity))
        }
        o["aggregation"] = JSON.enumValue(s.quantityType.aggregationStyle.rawValue, "HKQuantityAggregationStyle")
        guard s.count > 1 else { return }
        o["count"] = JSON(s.count)
        if let d = s as? HKDiscreteQuantitySample {
            o["minimum"] = q(d.minimumQuantity, unit)
            o["maximum"] = q(d.maximumQuantity, unit)
            o["average"] = q(d.averageQuantity, unit)
            o["most_recent"] = q(d.mostRecentQuantity, unit)
            o["most_recent_start"] = JSON(d.mostRecentQuantityDateInterval.start)
            o["most_recent_end"] = JSON(d.mostRecentQuantityDateInterval.end)
        } else if let c = s as? HKCumulativeQuantitySample {
            o["sum"] = q(c.sumQuantity, unit)
        }
        records += await quantitySeries(s, unit: unit)
    }

    /// The individual values of a series sample (count > 1).
    private func quantitySeries(_ s: HKQuantitySample, unit: HKUnit?) async -> [JSON] {
        let values: AsyncThrowingStream<(HKQuantity, DateInterval), Error> = store.safeStream { cont in
            HKQuantitySeriesSampleQuery(quantityType: s.quantityType, predicate: HKQuery.predicateForObject(with: s.uuid)) {
                _, quantity, interval, _, done, error in
                if let error { cont.finish(throwing: error); return }
                if let quantity, let interval { cont.yield((quantity, interval)) }
                if done { cont.finish() }
            }
        }
        var points: [JSON] = []
        var chunks = Chunker(sample: s, series: "quantity", extra: unit.map { ["unit": .string($0.unitString)] } ?? [:])
        do {
            for try await (quantity, interval) in values {
                var p: [String: JSON] = ["start": JSON(interval.start), "end": JSON(interval.end)]
                if let split = QuantityCodec.split(quantity, preferred: unit) {
                    p["value"] = .double(split.value)
                    if split.unit != unit?.unitString { p["unit"] = .string(split.unit) }
                }
                points.append(.object(p))
                if points.count == Self.seriesPointsPerChunk { chunks.add(&points) }
            }
        } catch {
            chunks.error = error.localizedDescription
        }
        return chunks.finish(&points)
    }

    private func category(_ c: HKCategorySample, into o: inout [String: JSON]) {
        o["value"] = JSON(c.value)
        let enumName = HealthTypes.named(c.categoryType.identifier)?.valueEnum
        if let enumName {
            o["value_enum"] = .string(enumName)
            o.put("value_label", JSON(HealthTypes.enumName(enumName, c.value)))
        }
    }

    // MARK: Workouts

    private func workout(_ w: HKWorkout, into o: inout [String: JSON]) async {
        let stats = await statistics(w.allStatistics)
        let energy = w.statistics(for: HKQuantityType(.activeEnergyBurned))?.sumQuantity()
        var distanceTypes: [HKQuantityTypeIdentifier] = [
            .distanceWalkingRunning, .distanceCycling, .distanceSwimming, .distanceDownhillSnowSports, .distanceWheelchair,
        ]
        if #available(iOS 18.0, *) {
            distanceTypes += [.distanceRowing, .distancePaddleSports, .distanceCrossCountrySkiing, .distanceSkatingSports]
        }
        let distance = distanceTypes.lazy.compactMap { w.statistics(for: HKQuantityType($0))?.sumQuantity() }.first
        // v1 summary, which the server forwards as metrics.
        var summary: [String: JSON] = [
            "activity_type": JSON(Int(w.workoutActivityType.rawValue)),
            "duration_s": .double(w.duration),
        ]
        summary.put("activity_name", JSON(w.workoutActivityType.v1Name))
        summary.put("energy_kcal", energy.map { .double($0.doubleValue(for: .kilocalorie())) })
        summary.put("distance_m", distance.map { .double($0.doubleValue(for: .meter())) })
        o["workout"] = .object(summary)

        o["activity_type"] = JSON.enumValue(Int(w.workoutActivityType.rawValue), "HKWorkoutActivityType")
        o["duration_s"] = .double(w.duration)
        o["statistics"] = stats
        o.put("events", w.workoutEvents.map { .array($0.map(event)) })
        var activities: [JSON] = []
        for a in w.workoutActivities {
            var ao: [String: JSON] = [
                "uuid": .string(a.uuid.uuidString),
                "start": JSON(a.startDate),
                "duration_s": .double(a.duration),
                "configuration": configuration(a.workoutConfiguration),
                "events": .array(a.workoutEvents.map(event)),
                "statistics": await statistics(a.allStatistics),
            ]
            ao.put("end", JSON(a.endDate))
            ao.put("metadata", JSON.metadata(a.metadata))
            activities.append(.object(ao))
        }
        o["activities"] = .array(activities)
        // Routes are synced as their own samples; record which belong to this workout.
        let routes = (try? await store.safeSamples(HKSeriesType.workoutRoute()) { HKQuery.predicateForObjects(from: w) }) ?? []
        if !routes.isEmpty { o["route_uuids"] = .array(routes.map { .string($0.uuid.uuidString) }) }
    }

    private func configuration(_ c: HKWorkoutConfiguration) -> JSON {
        var o: [String: JSON] = [
            "activity_type": JSON.enumValue(Int(c.activityType.rawValue), "HKWorkoutActivityType"),
            "location_type": JSON.enumValue(c.locationType.rawValue, "HKWorkoutSessionLocationType"),
            "swimming_location_type": JSON.enumValue(c.swimmingLocationType.rawValue, "HKWorkoutSwimmingLocationType"),
        ]
        o.put("lap_length", c.lapLength.map { q($0) })
        return .object(o)
    }

    private func event(_ e: HKWorkoutEvent) -> JSON {
        var o: [String: JSON] = [
            "type": JSON.enumValue(e.type.rawValue, "HKWorkoutEventType"),
            "start": JSON(e.dateInterval.start),
            "end": JSON(e.dateInterval.end),
        ]
        o.put("metadata", JSON.metadata(e.metadata))
        return .object(o)
    }

    private func statistics(_ all: [HKQuantityType: HKStatistics]) async -> JSON {
        var out: [JSON] = []
        for (type, s) in all.sorted(by: { $0.key.identifier < $1.key.identifier }) {
            let unit = await units.unit(for: type)
            var o: [String: JSON] = [
                "type": .string(type.identifier),
                "start": JSON(s.startDate),
                "end": JSON(s.endDate),
            ]
            let raised = HKRTry {
                o.put("sum", s.sumQuantity().map { q($0, unit) })
                o.put("average", s.averageQuantity().map { q($0, unit) })
                o.put("minimum", s.minimumQuantity().map { q($0, unit) })
                o.put("maximum", s.maximumQuantity().map { q($0, unit) })
                o.put("most_recent", s.mostRecentQuantity().map { q($0, unit) })
                if let i = s.mostRecentQuantityDateInterval() {
                    o["most_recent_start"] = JSON(i.start)
                    o["most_recent_end"] = JSON(i.end)
                }
                o.put("duration", s.duration().map { q($0, .second()) })
            }
            o.put("error", JSON(raised))
            out.append(.object(o))
        }
        return .array(out)
    }

    // MARK: Series

    private func route(_ r: HKWorkoutRoute) async -> [JSON] {
        var points: [JSON] = []
        var chunks = Chunker(sample: r, series: "workout_route")
        do {
            let locations: AsyncThrowingStream<CLLocation, Error> = store.safeStream { cont in
                HKWorkoutRouteQuery(route: r) { _, locations, done, error in
                    if let error { cont.finish(throwing: error); return }
                    for l in locations ?? [] { cont.yield(l) }
                    if done { cont.finish() }
                }
            }
            for try await l in locations {
                var p: [String: JSON] = [
                    "t": JSON(l.timestamp),
                    "lat": .double(l.coordinate.latitude),
                    "lon": .double(l.coordinate.longitude),
                    "alt": .double(l.altitude),
                    "h_acc": .double(l.horizontalAccuracy),
                    "v_acc": .double(l.verticalAccuracy),
                    "speed": .double(l.speed),
                    "speed_acc": .double(l.speedAccuracy),
                    "course": .double(l.course),
                    "course_acc": .double(l.courseAccuracy),
                    "ellipsoidal_alt": .double(l.ellipsoidalAltitude),
                ]
                p.put("floor", l.floor.map { JSON($0.level) })
                points.append(.object(p))
                if points.count == Self.routePointsPerChunk { chunks.add(&points) }
            }
        } catch {
            chunks.error = error.localizedDescription
        }
        return chunks.finish(&points)
    }

    private func heartbeats(_ h: HKHeartbeatSeriesSample) async -> [JSON] {
        var points: [JSON] = []
        var chunks = Chunker(sample: h, series: "heartbeat")
        do {
            let beats: AsyncThrowingStream<(TimeInterval, Bool), Error> = store.safeStream { cont in
                HKHeartbeatSeriesQuery(heartbeatSeries: h) { _, t, gap, done, error in
                    if let error { cont.finish(throwing: error); return }
                    cont.yield((t, gap))
                    if done { cont.finish() }
                }
            }
            var n = 0
            // An empty series answers once, with done and no beat: never send more than count.
            for try await (t, gap) in beats where n < h.count {
                n += 1
                points.append(["t": .double(t), "gap": .bool(gap)])
                if points.count == Self.heartbeatsPerChunk { chunks.add(&points) }
            }
        } catch {
            chunks.error = error.localizedDescription
        }
        return chunks.finish(&points)
    }

    private func electrocardiogram(_ e: HKElectrocardiogram, into o: inout [String: JSON]) async -> [JSON] {
        o["number_of_voltage_measurements"] = JSON(e.numberOfVoltageMeasurements)
        o.put("sampling_frequency", e.samplingFrequency.map { q($0, .hertz()) })
        o.put("average_heart_rate", e.averageHeartRate.map { q($0, .count().unitDivided(by: .minute())) })
        o["classification"] = JSON.enumValue(e.classification.rawValue, "HKElectrocardiogramClassification")
        o["symptoms_status"] = JSON.enumValue(e.symptomsStatus.rawValue, "HKElectrocardiogramSymptomsStatus")
        if e.symptomsStatus == .present {
            o["symptoms"] = .array(await symptoms(of: e))
        }

        let microvolts = HKUnit.voltUnit(with: .micro)
        var points: [JSON] = []
        var chunks = Chunker(sample: e, series: "electrocardiogram",
                             extra: ["unit": .string(microvolts.unitString), "lead": JSON.enumValue(1, "HKElectrocardiogramLead")])
        do {
            let measurements: AsyncThrowingStream<HKElectrocardiogram.VoltageMeasurement, Error> = store.safeStream { cont in
                HKElectrocardiogramQuery(e) { _, result in
                    switch result {
                    case let .measurement(m): cont.yield(m)
                    case .done: cont.finish()
                    case let .error(error): cont.finish(throwing: error)
                    @unknown default: cont.finish()
                    }
                }
            }
            for try await m in measurements {
                let v = m.quantity(for: .appleWatchSimilarToLeadI)?.doubleValue(for: microvolts)
                points.append(["t": .double(m.timeSinceSampleStart), "v": JSON(v)])
                if points.count == Self.voltagesPerChunk { chunks.add(&points) }
            }
        } catch {
            chunks.error = error.localizedDescription
        }
        return chunks.finish(&points)
    }

    /// Symptom samples logged with an ECG: UUID and type of each (they sync as category samples).
    private func symptoms(of e: HKElectrocardiogram) async -> [JSON] {
        var out: [JSON] = []
        let predicate = HKQuery.predicateForObjectsAssociated(electrocardiogram: e)
        for synced in HealthTypes.all where synced.valueEnum == "HKCategoryValueSeverity" || synced.valueEnum == "HKCategoryValuePresence" {
            guard let type = synced.type as? HKCategoryType else { continue }
            let found = (try? await store.safeSamples(type) { predicate }) ?? []
            for case let s as HKCategorySample in found {
                out.append(["uuid": .string(s.uuid.uuidString), "type": .string(type.identifier), "value": JSON(s.value)])
            }
        }
        return out
    }

    // MARK: Other kinds

    private func audiogramPoint(_ p: HKAudiogramSensitivityPoint) -> JSON {
        var o: [String: JSON] = ["frequency": q(p.frequency, .hertz())]
        let dBHL = HKUnit.decibelHearingLevel()
        if #available(iOS 18.1, *) {
            o["tests"] = .array(p.tests.map { t in
                var to: [String: JSON] = [
                    "sensitivity": q(t.sensitivity, dBHL),
                    "conduction": JSON.enumValue(t.type.rawValue, "HKAudiogramConductionType"),
                    "masked": .bool(t.masked),
                    "side": JSON.enumValue(t.side.rawValue, "HKAudiogramSensitivityTestSide"),
                ]
                if let r = t.clampingRange {
                    to["clamping_range"] = ["lower": q(r.lowerBound, dBHL), "upper": q(r.upperBound, dBHL)]
                }
                return .object(to)
            })
        } else {
            o.put("left_ear", p.leftEarSensitivity.map { q($0, dBHL) })
            o.put("right_ear", p.rightEarSensitivity.map { q($0, dBHL) })
        }
        return .object(o)
    }

    private func clinical(_ r: HKClinicalRecord, into o: inout [String: JSON]) {
        o["clinical_type"] = .string(r.clinicalType.identifier)
        o["display_name"] = .string(r.displayName)
        guard let fhir = r.fhirResource else { return }
        var f: [String: JSON] = [
            "resource_type": .string(fhir.resourceType.rawValue),
            "identifier": .string(fhir.identifier),
            "fhir_version": .string(fhir.fhirVersion.stringRepresentation),
        ]
        f.put("source_url", JSON(fhir.sourceURL?.absoluteString))
        if let parsed = try? JSONSerialization.jsonObject(with: fhir.data) {
            f["data"] = JSON.from(parsed)
        } else {
            f["data_base64"] = .string(fhir.data.base64EncodedString())
        }
        o["fhir"] = .object(f)
    }

    private func cda(_ d: HKCDADocumentSample, into o: inout [String: JSON]) async -> [JSON] {
        // Anchored queries return CDA samples without the document: fetch it.
        let doc: HKCDADocument? = await withCheckedContinuation { cont in
            let once = ResumeOnce(cont)
            let raised = store.safeExecute {
                HKDocumentQuery(
                    documentType: d.documentType, predicate: HKQuery.predicateForObject(with: d.uuid), limit: 1,
                    sortDescriptors: nil, includeDocumentData: true
                ) { _, results, done, _ in
                    if done || results?.isEmpty == false {
                        once.resume((results?.first as? HKCDADocumentSample)?.document)
                    }
                }
            }
            if raised != nil { once.resume(nil) }
        }
        guard let doc else { return [] }
        o["title"] = .string(doc.title)
        o["patient_name"] = .string(doc.patientName)
        o["author_name"] = .string(doc.authorName)
        o["custodian_name"] = .string(doc.custodianName)
        guard let data = doc.documentData else { return [] }
        o["document_bytes"] = JSON(data.count)
        return Self.blobChunks(owner: d, blob: "cda_document", id: "document", contentType: "application/xml", data: data)
    }

    private func vision(_ v: HKVisionPrescription, into o: inout [String: JSON]) {
        o["prescription_type"] = JSON.enumValue(Int(v.prescriptionType.rawValue), "HKVisionPrescriptionType")
        o["date_issued"] = JSON(v.dateIssued)
        o.put("expiration_date", JSON(v.expirationDate))
        if let g = v as? HKGlassesPrescription {
            o.put("right_eye", g.rightEye.map(glassesLens))
            o.put("left_eye", g.leftEye.map(glassesLens))
        } else if let c = v as? HKContactsPrescription {
            o["brand"] = .string(c.brand)
            o.put("right_eye", c.rightEye.map(contactsLens))
            o.put("left_eye", c.leftEye.map(contactsLens))
        }
    }

    private func lens(_ l: HKLensSpecification) -> [String: JSON] {
        var o: [String: JSON] = ["sphere": q(l.sphere)]
        o.put("cylinder", l.cylinder.map { q($0) })
        o.put("axis", l.axis.map { q($0) })
        o.put("add_power", l.addPower.map { q($0) })
        return o
    }

    private func glassesLens(_ l: HKGlassesLensSpecification) -> JSON {
        var o = lens(l)
        o.put("vertex_distance", l.vertexDistance.map { q($0) })
        o.put("far_pupillary_distance", l.farPupillaryDistance.map { q($0) })
        o.put("near_pupillary_distance", l.nearPupillaryDistance.map { q($0) })
        if let p = l.prism {
            o["prism"] = [
                "amount": q(p.amount), "angle": q(p.angle),
                "vertical_amount": q(p.verticalAmount), "horizontal_amount": q(p.horizontalAmount),
                "vertical_base": JSON.enumValue(p.verticalBase.rawValue, "HKPrismBase"),
                "horizontal_base": JSON.enumValue(p.horizontalBase.rawValue, "HKPrismBase"),
                "eye": JSON.enumValue(p.eye.rawValue, "HKVisionEye"),
            ]
        }
        return .object(o)
    }

    private func contactsLens(_ l: HKContactsLensSpecification) -> JSON {
        var o = lens(l)
        o.put("base_curve", l.baseCurve.map { q($0) })
        o.put("diameter", l.diameter.map { q($0) })
        return .object(o)
    }

    /// Files attached to a sample (scans of a vision prescription), as chunked records.
    private func attachments(for object: HKObject) async -> [JSON] {
        let attachStore = HKAttachmentStore(healthStore: store)
        let list: [HKAttachment] = await withCheckedContinuation { cont in
            let once = ResumeOnce(cont)
            let raised = HKRTry { attachStore.getAttachments(for: object) { a, _ in once.resume(a ?? []) } }
            if raised != nil { once.resume([]) }
        }
        var out: [JSON] = []
        for a in list {
            let data: Data? = await withCheckedContinuation { cont in
                let once = ResumeOnce(cont)
                let raised = HKRTry { _ = attachStore.getData(for: a) { data, _ in once.resume(data) } }
                if raised != nil { once.resume(nil) }
            }
            guard let data else { continue }
            out += Self.blobChunks(owner: object, blob: "attachment", id: a.identifier.uuidString,
                                   contentType: a.contentType.identifier, data: data,
                                   extra: [
                                       "name": .string(a.name),
                                       "creation_date": JSON(a.creationDate),
                                       "metadata": JSON.metadata(a.metadata) ?? .null,
                                   ])
        }
        return out
    }

    private func other(_ s: HKSample, into o: inout [String: JSON]) {
        if #available(iOS 18.0, *) {
            if let m = s as? HKStateOfMind {
                o["kind"] = "state_of_mind"
                o["state_kind"] = JSON.enumValue(m.kind.rawValue, "HKStateOfMindKind")
                o["valence"] = .double(m.valence)
                o["valence_classification"] = JSON.enumValue(m.valenceClassification.rawValue, "HKStateOfMindValenceClassification")
                o["labels"] = .array(m.labels.map { JSON.enumValue($0.rawValue, "HKStateOfMindLabel") })
                o["associations"] = .array(m.associations.map { JSON.enumValue($0.rawValue, "HKStateOfMindAssociation") })
                return
            }
            if let a = s as? HKScoredAssessment {
                o["kind"] = "scored_assessment"
                o["score"] = JSON(a.score)
                if let g = a as? HKGAD7Assessment {
                    o["risk"] = JSON.enumValue(g.risk.rawValue, "HKGAD7AssessmentRisk")
                    o["answers"] = .array(g.answers.map { JSON.enumValue($0.rawValue, "HKGAD7AssessmentAnswer") })
                } else if let p = a as? HKPHQ9Assessment {
                    o["risk"] = JSON.enumValue(p.risk.rawValue, "HKPHQ9AssessmentRisk")
                    o["answers"] = .array(p.answers.map { JSON.enumValue($0.rawValue, "HKPHQ9AssessmentAnswer") })
                }
                return
            }
        }
        if #available(iOS 26.0, *) {
            if let d = s as? HKMedicationDoseEvent {
                o["kind"] = "medication_dose_event"
                o["schedule_type"] = JSON.enumValue(d.scheduleType.rawValue, "HKMedicationDoseEventScheduleType")
                o["log_status"] = JSON.enumValue(d.logStatus.rawValue, "HKMedicationDoseEventLogStatus")
                o["medication_concept"] = Self.concept(d.medicationConceptIdentifier)
                o.put("scheduled_date", JSON(d.scheduledDate))
                o.put("scheduled_dose_quantity", JSON(d.scheduledDoseQuantity))
                o.put("dose_quantity", JSON(d.doseQuantity))
                o["unit"] = .string(d.unit.unitString)
                return
            }
        }
    }

    /// A medication concept identifier: its domain, description and archive (its only stable form).
    @available(iOS 26.0, *)
    static func concept(_ id: HKHealthConceptIdentifier) -> JSON {
        ["domain": .string(id.domain.rawValue), "description": .string(String(describing: id)), "archive": JSON.archive(id)]
    }

    // MARK: Chunking

    static func blobChunks(owner: HKObject, blob: String, id: String, contentType: String, data: Data,
                           extra: [String: JSON] = [:]) -> [JSON] {
        let total = max(1, (data.count + blobBytesPerChunk - 1) / blobBytesPerChunk)
        return (0..<total).map { i in
            let part = data.subdata(in: (i * blobBytesPerChunk)..<min(data.count, (i + 1) * blobBytesPerChunk))
            var o = extra
            o["kind"] = "blob"
            o["key"] = .string("\(owner.uuid.uuidString)/\(blob)/\(id)/\(i)")
            o["owner_uuid"] = .string(owner.uuid.uuidString)
            o["blob"] = .string(blob)
            o["id"] = .string(id)
            o["content_type"] = .string(contentType)
            o["index"] = JSON(i)
            o["chunks"] = JSON(total)
            o["total_bytes"] = JSON(data.count)
            o["base64"] = .string(part.base64EncodedString())
            return .object(o)
        }
    }
}

/// Splits a sample's series points into records: {kind: "series", key: "<uuid>/<series>/<i>",
/// index, final, points}. The last record has final=true and the total point count.
struct Chunker {
    let sample: HKSample
    let series: String
    var extra: [String: JSON] = [:]
    var error: String?
    private(set) var records: [JSON] = []
    private var total = 0

    init(sample: HKSample, series: String, extra: [String: JSON] = [:]) {
        self.sample = sample
        self.series = series
        self.extra = extra
    }

    mutating func add(_ points: inout [JSON], final: Bool = false) {
        total += points.count
        var o = extra
        o["kind"] = "series"
        o["key"] = .string("\(sample.uuid.uuidString)/\(series)/\(records.count)")
        o["sample_uuid"] = .string(sample.uuid.uuidString)
        o["sample_type"] = .string(sample.sampleType.identifier)
        o["series"] = .string(series)
        o["index"] = JSON(records.count)
        o["final"] = .bool(final)
        o["points"] = .array(points)
        if final {
            o["total_points"] = JSON(total)
            o.put("error", JSON(error))
        }
        records.append(.object(o))
        points.removeAll(keepingCapacity: true)
    }

    mutating func finish(_ points: inout [JSON]) -> [JSON] {
        add(&points, final: true)
        return records
    }
}
