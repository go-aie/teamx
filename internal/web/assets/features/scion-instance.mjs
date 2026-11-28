export const DEFAULT_SCION_ORIGIN = "https://scion.opencsg.com";

export function createScionInstanceOptions(document, id) {
  const options = document.createElement("datalist"), option = document.createElement("option");
  options.id = id;
  option.value = DEFAULT_SCION_ORIGIN;
  options.append(option);
  return options;
}
