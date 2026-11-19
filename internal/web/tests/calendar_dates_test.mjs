import test from "node:test";
import assert from "node:assert/strict";
import { calendarRange, calendarOffsetLabels, localDay, shiftDay, weekDates, monthDates, minuteOfDay, pointGroups } from "../assets/features/calendar-dates.mjs";

test("calendar navigation uses display-zone days across years and opposite UTC dates",()=>{
  assert.equal(localDay("2026-12-31T17:00:00Z","Asia/Shanghai"),"2027-01-01");
  assert.deepEqual(weekDates("2027-01-01"),["2026-12-28","2026-12-29","2026-12-30","2026-12-31","2027-01-01","2027-01-02","2027-01-03"]);
  assert.equal(shiftDay("2027-01-01",-1),"2026-12-31");
  const r=calendarRange("2027-01-01","day","Asia/Shanghai");
  assert.equal(Date.parse(r.from),Date.parse("2026-12-31T16:00:00Z"));
  assert.equal(Date.parse(r.to),Date.parse("2027-01-01T16:00:00Z"));
});
test("calendar range respects short and long DST days and weeks",()=>{
  for(const [date,mode,hours] of [["2026-03-08","day",23],["2026-11-01","day",25],["2026-03-08","week",167],["2026-11-01","agenda",169]]){
    const r=calendarRange(date,mode,"America/New_York");
    assert.equal((Date.parse(r.to)-Date.parse(r.from))/3600000,hours);
    assert.equal(localDay(r.from,"America/New_York"),r.dates[0]);
  }
});

test("compact time-zone labels follow the viewed range including DST and fractional offsets",()=>{
  assert.deepEqual(calendarOffsetLabels("2026-11-19","week","Asia/Shanghai"),["GMT+8"]);
  assert.deepEqual(calendarOffsetLabels("2026-01-15","day","America/New_York"),["GMT-5"]);
  assert.deepEqual(calendarOffsetLabels("2026-07-15","day","America/New_York"),["GMT-4"]);
  assert.deepEqual(calendarOffsetLabels("2026-03-08","week","America/New_York"),["GMT-5","GMT-4"]);
  assert.deepEqual(calendarOffsetLabels("2026-11-01","day","America/New_York"),["GMT-4","GMT-5"]);
  assert.deepEqual(calendarOffsetLabels("2026-11-19","day","Asia/Kathmandu"),["GMT+5:45"]);
});


test("month picker crosses years and retains a complete Monday-first grid",()=>{
 const month=monthDates("2026-12-31",1);
 assert.equal(month.month,"2027-01-01");assert.equal(month.dates.length,42);
 assert.equal(month.dates[0],"2026-12-28");assert.equal(month.dates.at(-1),"2027-02-07");
 assert.equal(monthDates("2027-01-31",-1).month,"2026-12-01");
});
test("minute positions follow the display zone and labels cluster without stretching the axis",()=>{
 const point=at=>({due_at:at});
 assert.equal(minuteOfDay("2026-11-19T01:30:00Z","Asia/Shanghai"),570);
 const items=[point("2026-11-19T02:00:00Z"),point("2026-11-19T01:00:00Z"),point("2026-11-19T01:30:00Z")];
 const groups=pointGroups(items,"Asia/Shanghai");
 assert.equal(groups.length,1);assert.equal(groups[0].top,504);assert.equal(groups[0].items.length,3);
 assert.equal(items[0].due_at,"2026-11-19T02:00:00Z");
 assert.equal(pointGroups([point("2026-11-19T01:30:00Z")],"Asia/Shanghai")[0].top,532);
 assert.equal(pointGroups([point("2026-11-19T01:00:00Z"),point("2026-11-19T03:00:00Z")],"Asia/Shanghai").length,2);
 assert.equal(minuteOfDay("2026-11-01T05:30:00Z","America/New_York"),minuteOfDay("2026-11-01T06:30:00Z","America/New_York"));
});
