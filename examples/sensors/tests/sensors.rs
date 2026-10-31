// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

//! The generated sensors client end to end: select the database, write two
//! minutes of readings from two sites through the stores, and read them back by
//! tag, window, newest point, count and per-minute aggregates.
//!
//! Always runs against Arrow captures in a temporary directory. With
//! INFLUX_HOST, INFLUX_TOKEN and INFLUX_DATABASE set it also mirrors every point
//! into InfluxDB 3, reads the same answers back from there, and drops the tables
//! it wrote.

use database::store::DB;
use sensors::sensors_db::{self, environment::*, fleet::*};
use std::time::Duration;

type Result<T> = std::result::Result<T, Box<dyn std::error::Error>>;

const SECOND: i64 = 1_000_000_000;

#[tokio::test]
async fn sensors_roundtrip() -> Result<()> {
    let root = std::env::temp_dir().join(format!("sensors-example-{}", std::process::id()));
    let arrow = sensors_db::open_arrow(&root).await?;
    let influx = match std::env::var("INFLUX_HOST") {
        Ok(_) => Some(sensors_db::connect_influx_env().await?),
        Err(_) => None,
    };
    let db = match &influx {
        Some(influx) => sensors_db::mirror(influx.clone(), arrow.clone()),
        None => arrow.clone(),
    };
    sensors_db::ensure_schema(&db).await?;

    let result = run(&db, &arrow, influx.as_ref()).await;
    db.close().await?;
    if let Some(influx) = &influx {
        for res in sensors_db::resources() {
            match influx.schema.drop_schema(res).await {
                Ok(()) | Err(database::Error::NotFound) => {}
                Err(e) => return Err(e.into()),
            }
        }
    }
    let _ = std::fs::remove_dir_all(&root);
    result
}

async fn run(db: &DB, arrow: &DB, influx: Option<&DB>) -> Result<()> {
    let now = std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)?
        .as_nanos() as i64;
    let start = now - now.rem_euclid(60 * SECOND) - 120 * SECOND;

    let mut readings = Vec::new();
    for s in 0..120 {
        for (device, site, base) in [("probe-1", "north", 10.0), ("probe-2", "south", 20.0)] {
            readings.push(ClimateReading {
                device_id: device.into(),
                site: site.into(),
                observe_time: start + s * SECOND,
                temperature_c: base + (s % 2) as f64,
                humidity_pct: 50.0,
                pressure_hpa: 1013.25,
                co2_ppm: 400 + s as u32,
                quality: ReadingQuality::Good.into(),
            });
        }
    }
    ClimateReadingStore::new(db).create_many(&readings).await?;
    DeviceHealthStore::new(db)
        .create(&DeviceHealth {
            device_id: "probe-1".into(),
            firmware: "2.4.1".into(),
            report_time: start,
            battery_pct: 87.5,
            rssi_dbm: -71,
            uptime_s: u64::MAX / 3,
            charging: false,
        })
        .await?;

    let mut backends = vec![arrow];
    backends.extend(influx);
    for read in backends {
        let climate = ClimateReadingStore::new(read);
        let window = || climate.query().between(start, start + 120 * SECOND);

        let north = window().site("north").all().await?;
        assert_eq!(north.len(), 120, "{}", read.backend);
        assert!(north.iter().all(|r| r.site == "north"));

        let newest = window()
            .device_id("probe-2")
            .latest()
            .await?
            .expect("a reading");
        assert_eq!(
            (newest.observe_time, newest.co2_ppm),
            (start + 119 * SECOND, 519)
        );
        assert_eq!(
            ReadingQuality::try_from(newest.quality),
            Ok(ReadingQuality::Good)
        );

        assert_eq!(window().site("south").count().await?, 120);

        let per_minute = window()
            .group_by(ClimateReadingTag::Site)
            .aggregate(
                Duration::from_secs(60),
                [
                    ClimateReadingField::TemperatureC.avg(),
                    ClimateReadingField::Co2Ppm.max(),
                ],
            )
            .await?;
        assert_eq!(per_minute.len(), 4, "two minutes × two sites");
        for bucket in &per_minute {
            let want = if bucket.group["site"] == "north" {
                10.5
            } else {
                20.5
            };
            assert_eq!(bucket.values["avg_temperature_c"], want);
        }

        let health = DeviceHealthStore::new(read)
            .query()
            .between(start, start + SECOND)
            .device_id("probe-1")
            .latest()
            .await?
            .expect("a report");
        assert_eq!(health.uptime_s, u64::MAX / 3);
        println!("{}: every query agrees", read.backend);
    }
    Ok(())
}

/// `buffered` accepts points while queued and makes every one durable by `close`.
#[tokio::test]
async fn buffered_writes_are_durable_after_close() -> Result<()> {
    let root = std::env::temp_dir().join(format!("sensors-buffered-{}", std::process::id()));
    let db = sensors_db::buffered(sensors_db::open_arrow(&root).await?)?;
    let store = GpsFixStore::new(&db);
    for i in 0..12_000 {
        let written = store
            .create(&GpsFix {
                device_id: "probe-1".into(),
                fix_time: i,
                latitude: 52.52,
                longitude: 13.405,
                altitude_m: 34.0,
                satellites: 9,
            })
            .await?;
        assert!(written.pending);
    }
    db.close().await?;

    let read = sensors_db::open_arrow(&root).await?;
    let fixes = GpsFixStore::new(&read)
        .query()
        .device_id("probe-1")
        .all()
        .await?;
    assert_eq!(fixes.len(), 12_000);
    read.close().await?;
    let _ = std::fs::remove_dir_all(&root);
    Ok(())
}
