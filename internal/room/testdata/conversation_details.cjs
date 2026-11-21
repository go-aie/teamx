// The conversation menu owns entry into the existing settings rail.
exports.toggleConversationDetails = async page => {
  await page.locator('#room-menu-label').click();
  await page.locator('#conversation-open-details').click();
};
