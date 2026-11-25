import { mountSpacePicker } from "./space-context.mjs";
import { mountSpaceReset } from "./space-reset.mjs";
import { createApp } from "./application.mjs";

await mountSpacePicker();
mountSpaceReset();
await createApp();
