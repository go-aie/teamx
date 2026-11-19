import { Temporal } from "../vendor/temporal.mjs";

export const shiftDay = (date, days) => Temporal.PlainDate.from(date).add({days}).toString();
export const shiftMonth = (date, months) => Temporal.PlainDate.from(date).add({months}).toString();
export function localDay(value, zone) {
  return Temporal.Instant.from(new Date(value).toISOString()).toZonedDateTimeISO(zone).toPlainDate().toString();
}
export function weekDates(date) {
  const start = shiftDay(date, 1 - Temporal.PlainDate.from(date).dayOfWeek);
  return Array.from({length:7}, (_, i) => shiftDay(start, i));
}
// Navigate dates in the display zone. A calendar week can be 167 or 169 hours.
export function calendarRange(date, mode, zone) {
  const dates = mode === "day" ? [date] : mode === "month" ? monthDates(date).dates : weekDates(date);
  const midnight = value => Temporal.PlainDate.from(value).toZonedDateTime(zone).toInstant().toString();
  return {dates, from:midnight(dates[0]), to:midnight(shiftDay(dates.at(-1), 1))};
}

// A range crossing a daylight-saving transition has two UTC offsets.
export function calendarOffsetLabels(date, mode, zone) {
  const range=calendarRange(date,mode,zone);
  const formatter=new Intl.DateTimeFormat("en-US",{timeZone:zone,timeZoneName:"shortOffset"});
  return [...new Set([Date.parse(range.from),Date.parse(range.to)-1].map(at=>
    formatter.formatToParts(at).find(part=>part.type==="timeZoneName").value))];
}

export function monthDates(date, offset=0) {
  const month=Temporal.PlainDate.from(date).with({day:1}).add({months:offset});
  const start=weekDates(month.toString())[0];
  return {month:month.toString(),dates:Array.from({length:42},(_,i)=>shiftDay(start,i))};
}
export function minuteOfDay(value, zone) {
  const local=Temporal.Instant.from(new Date(value).toISOString()).toZonedDateTimeISO(zone);
  return local.hour*60+local.minute;
}
// Point labels need a readable hit area, not a fabricated event duration.
// Colliding labels share a disclosure without stretching the time axis.
export function pointGroups(points, zone, hourHeight=56) {
  const groups=[];
  for(const item of [...points].sort((a,b)=>minuteOfDay(a.due_at,zone)-minuteOfDay(b.due_at,zone)||new Date(a.due_at)-new Date(b.due_at))){
    const top=minuteOfDay(item.due_at,zone)*hourHeight/60;
    const previous=groups.at(-1);
    if(previous&&top<previous.end){previous.items.push(item);previous.end=top+44;}
    else groups.push({top,end:top+44,items:[item]});
  }
  return groups;
}
